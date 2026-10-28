// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/pkg/ansible"
)

// links returns the URL builder of repoName's v3 API.
func (h *HTTPHandler) links(repoName string) ansible.Links {
	return ansible.Links{Root: h.apiPath(repoName)}
}

// pageParams reads limit and offset; ok is false, after a 400, for a value that
// is not a non-negative number.
func pageParams(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset, err := ansible.PageParams(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Invalid pagination.", err.Error())
		return 0, 0, false
	}

	return limit, offset, true
}

func versionRef(l ansible.Links, c *models.AnsibleCollection) ansible.VersionRef {
	return ansible.VersionRef{
		Version:         c.Version,
		Href:            l.Version(c.Namespace, c.Name, c.Version),
		CreatedAt:       ansible.Stamp(c.CreatedAt),
		UpdatedAt:       ansible.Stamp(c.UpdatedAt),
		RequiresAnsible: c.RequiresAnsible,
		Marks:           []string{},
	}
}

// newestFirst orders versions by descending precedence.
func newestFirst(rows []*models.AnsibleCollection) {
	slices.SortFunc(rows, func(a, b *models.AnsibleCollection) int { return ansible.CompareVersions(b.Version, a.Version) })
}

func (h *HTTPHandler) handleIndex(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	limit, offset, ok := pageParams(w, r)
	if !ok {
		return
	}

	names, total, err := h.DB.ListAnsibleCollectionNames(r.Context(), repo.ID, limit, offset)
	if err != nil {
		h.Log.Errorw("failed to list ansible collections", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return
	}

	l := h.links(repo.Name)

	docs := make([]ansible.CollectionDoc, len(names))
	for i, n := range names {
		docs[i] = ansible.CollectionDoc{
			Href:        l.Collection(n.Namespace, n.Name),
			Namespace:   n.Namespace,
			Name:        n.Name,
			VersionsURL: l.Versions(n.Namespace, n.Name),
			CreatedAt:   ansible.Stamp(n.CreatedAt),
			UpdatedAt:   ansible.Stamp(n.UpdatedAt),
		}
	}

	pg := ansible.NewPage(l.Collections(), total, limit, offset)
	pg.Data = docs
	h.writeDoc(w, r, repo, pg)
}

// versionsOf returns the live versions of the collection named in the URL,
// newest first, or writes a 404 (a collection without versions does not exist)
// and returns false.
func (h *HTTPHandler) versionsOf(w http.ResponseWriter, r *http.Request, repo *models.Repository) ([]*models.AnsibleCollection, bool) {
	namespace, name := param(r, "namespace"), param(r, "collection")
	if !ansible.ValidName(namespace) || !ansible.ValidName(name) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.", "")
		return nil, false
	}

	rows, err := h.DB.ListAnsibleCollectionVersions(r.Context(), repo.ID, namespace, name)
	if err != nil {
		h.Log.Errorw("failed to list ansible collection versions", "collection", namespace+"."+name, "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return nil, false
	}

	if len(rows) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Not found.", "No collection "+namespace+"."+name+".")
		return nil, false
	}

	newestFirst(rows)

	return rows, true
}

func (h *HTTPHandler) handleCollection(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	rows, ok := h.versionsOf(w, r, repo)
	if !ok {
		return
	}

	l := h.links(repo.Name)

	refs := make([]ansible.VersionRef, len(rows))
	for i, c := range rows {
		refs[i] = versionRef(l, c)
	}

	h.writeDoc(w, r, repo, ansible.NewCollectionDoc(l, rows[0].Namespace, rows[0].Name, refs))
}

func (h *HTTPHandler) handleVersions(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	limit, offset, ok := pageParams(w, r)
	if !ok {
		return
	}

	rows, ok := h.versionsOf(w, r, repo)
	if !ok {
		return
	}

	l := h.links(repo.Name)

	refs := make([]ansible.VersionRef, 0, min(limit, len(rows)))
	for _, c := range ansible.Window(rows, limit, offset) {
		refs = append(refs, versionRef(l, c))
	}

	pg := ansible.NewPage(l.Versions(rows[0].Namespace, rows[0].Name), len(rows), limit, offset)
	pg.Data = refs
	h.writeDoc(w, r, repo, pg)
}

// versionDoc builds the detail document of a stored version.
func (h *HTTPHandler) versionDoc(repoName string, c *models.AnsibleCollection) ansible.VersionDoc {
	meta := map[string]any{}
	if len(c.Metadata) > 0 {
		// An undecodable document leaves the metadata empty: the artifact and
		// its dependencies, which the client needs, are separate fields.
		_ = json.Unmarshal(c.Metadata, &meta)
	}

	meta["dependencies"] = c.Dependencies
	ref := versionRef(h.links(repoName), c)

	doc := ansible.VersionDoc{
		Version:         ref.Version,
		CreatedAt:       ref.CreatedAt,
		UpdatedAt:       ref.UpdatedAt,
		RequiresAnsible: ref.RequiresAnsible,
		Artifact:        ansible.ArtifactDoc{Filename: c.Filename, SHA256: c.SHA256, Size: c.SizeBytes},
		Collection:      ansible.CollectionRefDoc{Name: c.Name},
		Name:            c.Name,
		Namespace:       ansible.NamespaceDoc{Name: c.Namespace},
		Metadata:        meta,
	}
	doc.Relink(h.links(repoName))

	return doc
}

// lookupVersion returns the live version named in the URL, or writes a 404.
func (h *HTTPHandler) lookupVersion(w http.ResponseWriter, r *http.Request, repo *models.Repository) *models.AnsibleCollection {
	namespace, name, version := param(r, "namespace"), param(r, "collection"), param(r, "version")
	if !ansible.ValidName(namespace) || !ansible.ValidName(name) || !ansible.ValidVersion(version) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.", "")
		return nil
	}

	c, err := h.DB.GetAnsibleCollection(r.Context(), repo.ID, namespace, name, version)
	if err != nil {
		h.Log.Errorw("failed to get ansible collection", "collection", namespace+"."+name, "version", version, "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return nil
	}

	if c == nil {
		writeError(w, http.StatusNotFound, "not_found", "Not found.", "No collection "+namespace+"."+name+" "+version+".")
		return nil
	}

	return c
}

func (h *HTTPHandler) handleVersion(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	if c := h.lookupVersion(w, r, repo); c != nil {
		h.writeDoc(w, r, repo, h.versionDoc(repo.Name, c))
	}
}

// importDoc is the import task document the client polls after a publish.
type importDoc struct {
	ID         string        `json:"id"`
	State      string        `json:"state"`
	CreatedAt  string        `json:"created_at"`
	UpdatedAt  string        `json:"updated_at"`
	StartedAt  string        `json:"started_at"`
	FinishedAt string        `json:"finished_at"`
	Error      any           `json:"error"`
	Messages   []importEntry `json:"messages"`
	Namespace  string        `json:"namespace"`
	Name       string        `json:"name"`
	Version    string        `json:"version"`
}

type importEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Time    int64  `json:"time"`
}

// importTask is the finished import of a stored version. An import is validated
// before the artifact is stored, so every task that exists has completed; the
// task id is the id of the stored version.
func importTask(c *models.AnsibleCollection) importDoc {
	at := ansible.Stamp(c.CreatedAt)

	return importDoc{
		ID:         c.ID,
		State:      "completed",
		CreatedAt:  at,
		UpdatedAt:  at,
		StartedAt:  at,
		FinishedAt: at,
		Messages: []importEntry{
			{
				Level:   "INFO",
				Message: "Collection " + ansible.FQCN(c.Namespace, c.Name) + " " + c.Version + " imported",
				Time:    c.CreatedAt.Unix(),
			},
		},
		Namespace: c.Namespace,
		Name:      c.Name,
		Version:   c.Version,
	}
}

func (h *HTTPHandler) handleImport(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	id := param(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Not found.", "")
		return
	}

	c, err := h.DB.GetAnsibleCollectionByID(r.Context(), repo.ID, id)
	if err != nil {
		h.Log.Errorw("failed to get ansible import", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return
	}

	if c == nil {
		writeError(w, http.StatusNotFound, "not_found", "Not found.", "")
		return
	}

	h.writeDoc(w, r, repo, importTask(c))
}

func (h *HTTPHandler) handleDownload(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	filename := param(r, "filename")

	namespace, name, version, ok := ansible.ParseFileName(filename)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "Not found.", "")
		return
	}

	c, err := h.DB.GetAnsibleCollection(r.Context(), repo.ID, namespace, name, version)
	if err != nil {
		h.Log.Errorw("failed to get ansible collection", "filename", filename, "error", err)
		writeError(w, http.StatusInternalServerError, "error", "Internal error.", "")

		return
	}

	if c == nil {
		writeError(w, http.StatusNotFound, "not_found", "Not found.", "")
		return
	}

	if h.DenyIfQuarantined(w, r, repo.ID, quarantine.PathKeys(c.ID, filename, r.URL.Path)...) {
		return
	}

	if base.Revalidate(w, r, repo.Public, `"`+c.SHA256+`"`, artifactMaxAge) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{
		SHA256: c.BlobKey, Size: c.SizeBytes, Filename: filename, ContentType: "application/gzip", Repository: repo,
	}) {
		return
	}

	h.serveBlob(w, r, c)
}
