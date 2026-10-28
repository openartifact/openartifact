// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/pkg/ansible"
)

const (
	// defaultLimit is the page size of a list without a limit parameter.
	defaultLimit = 100
	// maxLimit is the largest page a client can ask for.
	maxLimit = 1000
)

// timeFormat is the timestamp layout of the Galaxy API (UTC, microseconds).
const timeFormat = "2006-01-02T15:04:05.000000Z"

func stamp(t time.Time) string { return t.UTC().Format(timeFormat) }

// page is the pagination envelope of the Galaxy API v3.
type page struct {
	Meta  map[string]int `json:"meta"`
	Links map[string]any `json:"links"`
	Data  any            `json:"data"`
}

// newPage cuts the window [offset, offset+limit) out of count items; base is
// listURL is the list URL without a query. The caller fills Data with the window.
func newPage(listURL string, count, limit, offset int) page {
	link := func(off int) string {
		return listURL + "?limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(off)
	}

	links := map[string]any{"first": link(0), "previous": nil, "next": nil, "last": link(max(count-1, 0) / limit * limit)}

	if offset > 0 {
		links["previous"] = link(max(offset-limit, 0))
	}

	if offset+limit < count {
		links["next"] = link(offset + limit)
	}

	return page{Meta: map[string]int{"count": count}, Links: links}
}

// pageParams reads limit and offset; ok is false, after a 400, for a value that
// is not a non-negative number.
func pageParams(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset := defaultLimit, 0

	for _, p := range []struct {
		key string
		dst *int
		min int
	}{{"limit", &limit, 1}, {"offset", &offset, 0}} {
		raw := r.URL.Query().Get(p.key)
		if raw == "" {
			continue
		}

		n, err := strconv.Atoi(raw)
		if err != nil || n < p.min {
			writeError(
				w,
				http.StatusBadRequest,
				"invalid",
				"Invalid pagination.",
				p.key+" must be a number of at least "+strconv.Itoa(p.min),
			)
			return 0, 0, false
		}

		*p.dst = n
	}

	return min(limit, maxLimit), offset, true
}

// window returns items[offset:offset+limit] clamped to the slice.
func window[T any](items []T, limit, offset int) []T {
	if offset >= len(items) {
		return nil
	}

	return items[offset:min(offset+limit, len(items))]
}

func (h *HTTPHandler) collectionPath(repoName, namespace, name string) string {
	return fmt.Sprintf("%s/collections/%s/%s/", h.apiPath(repoName), namespace, name)
}

func (h *HTTPHandler) versionPath(repoName, namespace, name, version string) string {
	return h.collectionPath(repoName, namespace, name) + "versions/" + url.PathEscape(version) + "/"
}

func (h *HTTPHandler) downloadPath(repoName, filename string) string {
	return h.apiPath(repoName) + "/artifacts/collections/" + url.PathEscape(filename)
}

// versionRef is an entry of a version list.
type versionRef struct {
	Version         string   `json:"version"`
	Href            string   `json:"href"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	RequiresAnsible string   `json:"requires_ansible,omitempty"`
	Marks           []string `json:"marks"`
}

func (h *HTTPHandler) versionRef(repoName string, c *models.AnsibleCollection) versionRef {
	return versionRef{
		Version:         c.Version,
		Href:            h.versionPath(repoName, c.Namespace, c.Name, c.Version),
		CreatedAt:       stamp(c.CreatedAt),
		UpdatedAt:       stamp(c.UpdatedAt),
		RequiresAnsible: c.RequiresAnsible,
		Marks:           []string{},
	}
}

// newestFirst orders versions by descending precedence.
func newestFirst(rows []*models.AnsibleCollection) {
	slices.SortFunc(rows, func(a, b *models.AnsibleCollection) int { return ansible.CompareVersions(b.Version, a.Version) })
}

// highest picks the highest release of rows (ordered newest first), or the
// highest pre-release when there is no release.
func highest(rows []*models.AnsibleCollection) *models.AnsibleCollection {
	for _, c := range rows {
		if !ansible.IsPrerelease(c.Version) {
			return c
		}
	}

	return rows[0]
}

// collectionDoc is the Galaxy document of a collection.
type collectionDoc struct {
	Href           string      `json:"href"`
	Namespace      string      `json:"namespace"`
	Name           string      `json:"name"`
	Deprecated     bool        `json:"deprecated"`
	VersionsURL    string      `json:"versions_url"`
	HighestVersion *versionTag `json:"highest_version,omitempty"`
	CreatedAt      string      `json:"created_at"`
	UpdatedAt      string      `json:"updated_at"`
}

type versionTag struct {
	Href    string `json:"href"`
	Version string `json:"version"`
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

	docs := make([]collectionDoc, len(names))
	for i, n := range names {
		docs[i] = collectionDoc{
			Href:        h.collectionPath(repo.Name, n.Namespace, n.Name),
			Namespace:   n.Namespace,
			Name:        n.Name,
			VersionsURL: h.collectionPath(repo.Name, n.Namespace, n.Name) + "versions/",
			CreatedAt:   stamp(n.CreatedAt),
			UpdatedAt:   stamp(n.UpdatedAt),
		}
	}

	pg := newPage(h.apiPath(repo.Name)+"/collections/", total, limit, offset)
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

	top := highest(rows)
	created, updated := rows[0].CreatedAt, rows[0].UpdatedAt

	for _, c := range rows {
		if c.CreatedAt.Before(created) {
			created = c.CreatedAt
		}

		if c.UpdatedAt.After(updated) {
			updated = c.UpdatedAt
		}
	}

	path := h.collectionPath(repo.Name, top.Namespace, top.Name)

	h.writeDoc(w, r, repo, collectionDoc{
		Href:           path,
		Namespace:      top.Namespace,
		Name:           top.Name,
		VersionsURL:    path + "versions/",
		HighestVersion: &versionTag{Href: h.versionPath(repo.Name, top.Namespace, top.Name, top.Version), Version: top.Version},
		CreatedAt:      stamp(created),
		UpdatedAt:      stamp(updated),
	})
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

	refs := make([]versionRef, 0, min(limit, len(rows)))
	for _, c := range window(rows, limit, offset) {
		refs = append(refs, h.versionRef(repo.Name, c))
	}

	pg := newPage(h.collectionPath(repo.Name, rows[0].Namespace, rows[0].Name)+"versions/", len(rows), limit, offset)
	pg.Data = refs
	h.writeDoc(w, r, repo, pg)
}

// versionDoc is the Galaxy document of one collection version.
type versionDoc struct {
	Version         string            `json:"version"`
	Href            string            `json:"href"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
	RequiresAnsible string            `json:"requires_ansible,omitempty"`
	Marks           []string          `json:"marks"`
	Artifact        artifactDoc       `json:"artifact"`
	Collection      collectionRefDoc  `json:"collection"`
	DownloadURL     string            `json:"download_url"`
	Name            string            `json:"name"`
	Namespace       namespaceDoc      `json:"namespace"`
	Metadata        map[string]any    `json:"metadata"`
	Signatures      []json.RawMessage `json:"signatures"`
}

type artifactDoc struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

type collectionRefDoc struct {
	Name string `json:"name"`
	Href string `json:"href"`
}

type namespaceDoc struct {
	Name string `json:"name"`
}

// versionDoc builds the detail document of a stored version.
func (h *HTTPHandler) versionDoc(repoName string, c *models.AnsibleCollection) versionDoc {
	meta := map[string]any{}
	if len(c.Metadata) > 0 {
		// An undecodable document leaves the metadata empty: the artifact and
		// its dependencies, which the client needs, are separate fields.
		_ = json.Unmarshal(c.Metadata, &meta)
	}

	meta["dependencies"] = c.Dependencies
	ref := h.versionRef(repoName, c)

	return versionDoc{
		Version:         ref.Version,
		Href:            ref.Href,
		CreatedAt:       ref.CreatedAt,
		UpdatedAt:       ref.UpdatedAt,
		RequiresAnsible: ref.RequiresAnsible,
		Marks:           ref.Marks,
		Artifact:        artifactDoc{Filename: c.Filename, SHA256: c.SHA256, Size: c.SizeBytes},
		Collection:      collectionRefDoc{Name: c.Name, Href: h.collectionPath(repoName, c.Namespace, c.Name)},
		DownloadURL:     h.downloadPath(repoName, c.Filename),
		Name:            c.Name,
		Namespace:       namespaceDoc{Name: c.Namespace},
		Metadata:        meta,
		Signatures:      []json.RawMessage{},
	}
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
	at := stamp(c.CreatedAt)

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
