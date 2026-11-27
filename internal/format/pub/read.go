// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/urlparams"
	pkgpub "github.com/openartifact/openartifact/pkg/pub"
)

// artifactMaxAge is how long shared caches may keep a public archive: it never
// changes under its file name.
const artifactMaxAge = 3600

// entry is the document entry of a stored version, its archive URL built below origin.
func entry(c *models.PubPackage) *pkgpub.Version {
	return pkgpub.NewVersion(c.Version, "", c.SHA256, c.CreatedAt, c.Pubspec)
}

// writeDoc answers with a package or version document, revalidated by the digest
// of its text, which carries the origin its absolute links were built from.
func (h *HTTPHandler) writeDoc(w http.ResponseWriter, r *http.Request, repo *models.Repository, body []byte) {
	if base.Revalidate(w, r, repo.Public, base.ContentETag(body), metadataMaxAge) {
		return
	}

	w.Header().Set("Content-Type", pkgpub.ContentType)
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: a JSON document with a JSON media type and nosniff, not HTML
	_, _ = w.Write(body)
}

func (h *HTTPHandler) handlePackage(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	name := param(r, "package")
	if !pkgpub.ValidName(name) {
		writeError(w, http.StatusNotFound, "NotFound", "Package "+strconv.Quote(name)+" does not exist.")
		return
	}

	rows, err := h.DB.ListPubVersions(r.Context(), repo.ID, name)
	if err != nil {
		h.Log.Errorw("failed to list pub versions", "package", name, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")

		return
	}

	if len(rows) == 0 {
		writeError(w, http.StatusNotFound, "NotFound", "Package "+strconv.Quote(name)+" does not exist.")
		return
	}

	slices.SortStableFunc(rows, func(a, b *models.PubPackage) int { return pkgpub.CompareVersions(a.Version, b.Version) })

	origin := h.PublicBase(w, r)
	versions := make([]*pkgpub.Version, len(rows))

	for i, c := range rows {
		versions[i] = entry(c)
	}

	body, err := pkgpub.NewDoc(name, versions).Marshal(
		func(v *pkgpub.Version) string { return archiveURL(origin, repo.Name, name, v.Version) }, nil)
	if err != nil {
		h.Log.Errorw("failed to encode pub package document", "package", name, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")

		return
	}

	h.writeDoc(w, r, repo, body)
}

func (h *HTTPHandler) handleVersion(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	name, version := param(r, "package"), param(r, "version")

	c := h.lookup(w, r, repo, name, version)
	if c == nil {
		return
	}

	origin := h.PublicBase(w, r)

	body, err := pkgpub.MarshalVersion(entry(c), func(*pkgpub.Version) string { return archiveURL(origin, repo.Name, name, version) })
	if err != nil {
		h.Log.Errorw("failed to encode pub version document", "package", name, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")

		return
	}

	h.writeDoc(w, r, repo, body)
}

// lookup returns the live version, answering 404 or 500 itself and returning nil
// when there is none.
func (h *HTTPHandler) lookup(w http.ResponseWriter, r *http.Request, repo *models.Repository, name, version string) *models.PubPackage {
	if !pkgpub.ValidName(name) || !pkgpub.ValidVersion(version) {
		writeError(w, http.StatusNotFound, "NotFound", "Package version not found.")
		return nil
	}

	c, err := h.DB.GetPubPackage(r.Context(), repo.ID, name, version)
	if err != nil {
		h.Log.Errorw("failed to get pub package", "package", name, "version", version, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")

		return nil
	}

	if c == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Package version not found.")
		return nil
	}

	return c
}

func (h *HTTPHandler) handleArchive(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	file := param(r, "file")

	name, version, ok := pkgpub.ParseArchiveName(file)
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Not found.")
		return
	}

	c := h.lookup(w, r, repo, name, version)
	if c == nil {
		return
	}

	if h.DenyIfQuarantined(w, r, repo.ID, quarantine.PathKeys(c.ID, file, r.URL.Path)...) {
		return
	}

	if base.Revalidate(w, r, repo.Public, `"`+c.SHA256+`"`, artifactMaxAge) {
		return
	}

	if h.TryPresign(w, r, presign.Blob{
		SHA256: c.BlobKey, Size: c.SizeBytes, Filename: file, ContentType: "application/gzip", Repository: repo,
	}) {
		return
	}

	blob, err := h.BlobStore.Get(r.Context(), c.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get pub blob", "key", c.BlobKey, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Internal error.")

		return
	}
	defer base.CloseLog(blob, h.Log)

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.FormatInt(c.SizeBytes, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+c.Filename+`"`)

	if _, err := iocopy.Copy(w, blob); err != nil {
		h.Log.Warnw("failed to stream pub archive", "filename", c.Filename, "error", err)
	}
}

// param returns the unescaped URL parameter: the router hands out the raw path.
func param(r *http.Request, key string) string {
	raw := urlparams.Vars(r)[key]

	v, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}

	return v
}
