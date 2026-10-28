// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/pkg/composer"
)

// maxSearchResults bounds the results one search request returns.
const maxSearchResults = 100

// maxFilterLen bounds the filter of a list request and the query of a search.
const maxFilterLen = 255

// handleRoot serves packages.json: where the per-package metadata is and which
// packages the repository has, so Composer never asks for a name it lacks.
func (h *HTTPHandler) handleRoot(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	names, err := h.DB.ListComposerPackageNames(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to list composer packages", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	available := make([]string, len(names))
	for i, n := range names {
		available[i] = n.Name
	}

	root := h.repoURL(repo.Name)

	h.writeDoc(w, r, repo, map[string]any{
		"packages":           []any{},
		"metadata-url":       root + "/p2/%package%.json",
		"available-packages": available,
		"list":               root + "/list.json",
		"search":             root + "/search.json?q=%query%&type=%type%",
	})
}

// handleMetadata serves p2/vendor/name.json (the stable and pre-release
// versions) and p2/vendor/name~dev.json (branches and numeric "-dev"
// versions), minified as Composer's MetadataMinifier does.
func (h *HTTPHandler) handleMetadata(w http.ResponseWriter, r *http.Request, repo *models.Repository, rt route) {
	rows, err := h.DB.ListComposerPackageVersions(r.Context(), repo.ID, rt.name)
	if err != nil {
		h.Log.Errorw("failed to list composer versions", "package", rt.name, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	// Newest upload first, the order Packagist serves.
	versions := make([]composer.Version, 0, len(rows))

	for _, row := range slices.Backward(rows) {
		if composer.IsDev(row.VersionNormalized) != rt.dev {
			continue
		}

		v, err := composer.NewVersion(row.Name, row.ComposerJSON, composer.Release{
			Version: row.Version, Normalized: row.VersionNormalized, Time: row.CreatedAt,
			Dist: composer.Dist{URL: h.distURL(repo.Name, row), SHA1: row.SHA1, Reference: row.SHA256},
		})
		if err != nil {
			h.Log.Errorw("failed to build composer version", "package", row.Name, "version", row.Version, "error", err)
			writeError(w, http.StatusInternalServerError, "Internal error.")

			return
		}

		versions = append(versions, v)
	}

	if len(versions) == 0 {
		writeError(w, http.StatusNotFound, "Not found.")
		return
	}

	body, err := composer.EncodeDocument(map[string][]composer.Version{rt.name: versions}, true)
	if err != nil {
		h.Log.Errorw("failed to encode composer metadata", "package", rt.name, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	h.writeBody(w, r, repo, body)
}

// filterRegexp turns a list filter into a pattern: "*" matches anything, the
// rest is literal and the match is case-insensitive and anchored.
func filterRegexp(filter string) (*regexp.Regexp, error) {
	parts := strings.Split(filter, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}

	return regexp.Compile("(?i)^" + strings.Join(parts, ".*") + "$") //nolint:wrapcheck // quoted input always compiles
}

// handleList serves list.json: the package names of the repository, narrowed by
// the optional "filter" (for example "vendor/*").
func (h *HTTPHandler) handleList(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	filter := r.URL.Query().Get("filter")
	if len(filter) > maxFilterLen {
		writeError(w, http.StatusBadRequest, "The filter is too long.")
		return
	}

	names, err := h.DB.ListComposerPackageNames(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to list composer packages", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	re, err := filterRegexp(filter)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid filter.")
		return
	}

	out := make([]string, 0, len(names))

	for _, n := range names {
		if filter == "" || re.MatchString(n.Name) {
			out = append(out, n.Name)
		}
	}

	h.writeDoc(w, r, repo, map[string]any{"packageNames": out})
}

// searchResult is one hit of search.json.
type searchResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// handleSearch serves search.json: the packages whose name or description
// contains the query, case-insensitively.
func (h *HTTPHandler) handleSearch(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len(q) > maxFilterLen {
		writeError(w, http.StatusBadRequest, "The query is too long.")
		return
	}

	names, err := h.DB.ListComposerPackageNames(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to list composer packages", "repo", repo.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	results := make([]searchResult, 0, min(len(names), maxSearchResults))
	total := 0

	for _, n := range names {
		if q != "" && !strings.Contains(strings.ToLower(n.Name), q) && !strings.Contains(strings.ToLower(n.Description), q) {
			continue
		}

		total++

		if len(results) < maxSearchResults {
			results = append(
				results,
				searchResult{Name: n.Name, Description: n.Description, URL: h.repoURL(repo.Name) + "/p2/" + n.Name + ".json"},
			)
		}
	}

	h.writeDoc(w, r, repo, map[string]any{"results": results, "total": total})
}

// distFileName is the name a dist is saved under: "vendor-package-version.zip".
func distFileName(p *models.ComposerPackage) string {
	return strings.NewReplacer("/", "-", "\\", "-", `"`, "").Replace(p.Name+"-"+p.Version) + ".zip"
}

// handleDist streams the zip of one version. The content reference in the URL
// must be the zip's sha256: a stale or forged URL is a 404.
func (h *HTTPHandler) handleDist(w http.ResponseWriter, r *http.Request, repo *models.Repository, rt route) {
	normalized, err := composer.NormalizePublished(rt.version)
	if err != nil {
		writeError(w, http.StatusNotFound, "Not found.")
		return
	}

	p, err := h.DB.GetComposerPackage(r.Context(), repo.ID, rt.name, normalized)
	if err != nil {
		h.Log.Errorw("failed to get composer package", "package", rt.name, "version", rt.version, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}

	if p == nil || p.SHA256 != rt.ref {
		writeError(w, http.StatusNotFound, "Not found.")
		return
	}

	if h.DenyIfQuarantined(w, r, repo.ID, quarantine.PathKeys(p.ID, distFileName(p), p.Name+"/"+p.Version, r.URL.Path)...) {
		return
	}

	if base.Revalidate(w, r, repo.Public, `"`+p.SHA256+`"`, distMaxAge) {
		return
	}

	h.serveBlob(w, r, p)
}

// serveBlob streams the zip of p.
func (h *HTTPHandler) serveBlob(w http.ResponseWriter, r *http.Request, p *models.ComposerPackage) {
	blob, err := h.BlobStore.Get(r.Context(), p.BlobKey)
	if err != nil {
		h.Log.Errorw("failed to get composer blob", "key", p.BlobKey, "error", err)
		writeError(w, http.StatusInternalServerError, "Internal error.")

		return
	}
	defer base.CloseLog(blob, h.Log)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(p.SizeBytes, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+url.PathEscape(distFileName(p))+`"`)

	if r.Method == http.MethodHead {
		return
	}

	if _, err := iocopy.Copy(w, blob); err != nil {
		h.Log.Warnw("failed to stream composer dist", "package", p.Name, "version", p.Version, "error", err)
	}
}
