// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/urlparams"
)

// This file is the read side of the registry protocols: version lists,
// module details and the calls that mint download tickets.

// pathPrefix is the path of the server's base URL (the part before /repos
// when the server is published under a prefix).
func (h *HTTPHandler) pathPrefix() string {
	u, err := url.Parse(h.BaseURL)
	if err != nil {
		return ""
	}

	return strings.TrimSuffix(u.EscapedPath(), "/")
}

// moduleCoords reads and validates {mod}/{system} from the URL. A name no
// upload could have used cannot exist, so it is a 404.
func moduleCoords(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	vars := urlparams.Vars(r)
	name, system := vars["mod"], vars["system"]

	if !validName(name) || !validName(system) {
		writeErrors(w, http.StatusNotFound, "module not found")
		return "", "", false
	}

	return name, system, true
}

// moduleVersionJSON is the module registry's description of one version.
type moduleVersionJSON struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Source      string `json:"source"`
	PublishedAt string `json:"published_at"`
	Downloads   int64  `json:"downloads"`
	Verified    bool   `json:"verified"`
}

func newModuleVersionJSON(repoName string, m *models.TerraformModule) moduleVersionJSON {
	return moduleVersionJSON{
		ID:          strings.Join([]string{repoName, m.Name, m.System, m.Version}, "/"),
		Namespace:   repoName,
		Name:        m.Name,
		Provider:    m.System,
		Version:     m.Version,
		PublishedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		Downloads:   m.DownloadCount,
	}
}

// sortedModules returns the versions of a module, lowest precedence first.
func sortedModules(mods []*models.TerraformModule) []*models.TerraformModule {
	out := slices.Clone(mods)
	slices.SortStableFunc(out, func(a, b *models.TerraformModule) int { return compareVersions(a.Version, b.Version) })

	return out
}

// handleModuleVersions answers GET /{mod}/{system}/versions.
func (h *HTTPHandler) handleModuleVersions(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	name, system, ok := moduleCoords(w, r)
	if !ok {
		return
	}

	stamp, err := h.DB.TerraformModuleStamp(r.Context(), repo.ID, name, system)
	if err != nil {
		h.Log.Errorw("failed to stamp terraform module", "repo", repo.Name, "module", name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if emptyStamp(stamp) {
		writeErrors(w, http.StatusNotFound, "module not found")
		return
	}

	if base.Revalidate(w, r, repo.Public, versionsETag(repo.ID, "module", name+"/"+system, stamp), versionsMaxAge) {
		return
	}

	mods, err := h.DB.ListTerraformModuleVersions(r.Context(), repo.ID, name, system)
	if err != nil {
		h.Log.Errorw("failed to list terraform module versions", "repo", repo.Name, "module", name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if len(mods) == 0 {
		writeErrors(w, http.StatusNotFound, "module not found")
		return
	}

	type versionEntry struct {
		Version string `json:"version"`
	}

	versions := make([]versionEntry, 0, len(mods))
	for _, m := range sortedModules(mods) {
		versions = append(versions, versionEntry{Version: m.Version})
	}

	type moduleEntry struct {
		Source   string         `json:"source"`
		Versions []versionEntry `json:"versions"`
	}

	writeJSON(w, http.StatusOK, map[string][]moduleEntry{
		"modules": {{Source: strings.Join([]string{repo.Name, name, system}, "/"), Versions: versions}},
	})
}

// latestModule returns the module version a client gets for "latest", or nil
// after writing the error response.
func (h *HTTPHandler) latestModule(w http.ResponseWriter, r *http.Request, repo *models.Repository, name, system string,
) *models.TerraformModule {
	mods, err := h.DB.ListTerraformModuleVersions(r.Context(), repo.ID, name, system)
	if err != nil {
		h.Log.Errorw("failed to list terraform module versions", "repo", repo.Name, "module", name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return nil
	}

	versions := make([]string, 0, len(mods))
	for _, m := range mods {
		versions = append(versions, m.Version)
	}

	latest := latestVersion(versions)
	for _, m := range mods {
		if m.Version == latest {
			return m
		}
	}

	writeErrors(w, http.StatusNotFound, "module not found")

	return nil
}

// handleModuleLatest answers GET /{mod}/{system}: the details of the latest
// version (reached through LatestGuard).
func (h *HTTPHandler) handleModuleLatest(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	name, system, ok := moduleCoords(w, r)
	if !ok {
		return
	}

	if m := h.latestModule(w, r, repo, name, system); m != nil {
		w.Header().Set("Cache-Control", "private, no-cache")
		writeJSON(w, http.StatusOK, newModuleVersionJSON(repo.Name, m))
	}
}

// handleModuleVersion answers GET /{mod}/{system}/{version}.
func (h *HTTPHandler) handleModuleVersion(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	name, system, ok := moduleCoords(w, r)
	if !ok {
		return
	}

	version := urlparams.Vars(r)["version"]
	if !validVersion(version) {
		writeErrors(w, http.StatusNotFound, "module version not found")
		return
	}

	m, err := h.DB.GetTerraformModule(r.Context(), repo.ID, name, system, version)
	if err != nil {
		h.Log.Errorw("failed to get terraform module", "repo", repo.Name, "module", name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if m == nil {
		writeErrors(w, http.StatusNotFound, "module version not found")
		return
	}

	w.Header().Set("Cache-Control", "private, no-cache")
	writeJSON(w, http.StatusOK, newModuleVersionJSON(repo.Name, m))
}

// handleModuleLatestDownload answers GET /{mod}/{system}/download with a
// redirect to the download call of the latest version.
func (h *HTTPHandler) handleModuleLatestDownload(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	name, system, ok := moduleCoords(w, r)
	if !ok {
		return
	}

	m := h.latestModule(w, r, repo, name, system)
	if m == nil {
		return
	}

	// The coordinates are validated names and a stored version, and the host
	// is the configured base path: a plain 302 with a relative Location.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Location", h.pathPrefix()+registryBasePath(repo.Name)+"/"+name+"/"+system+"/"+m.Version+"/download")
	w.WriteHeader(http.StatusFound)
}

// handleModuleDownload answers GET /{mod}/{system}/{version}/download: 204
// with X-Terraform-Get naming the ticketed archive URL. This is the call that
// RBAC, quarantine and the download counter see.
func (h *HTTPHandler) handleModuleDownload(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	name, system, ok := moduleCoords(w, r)
	if !ok {
		return
	}

	version := urlparams.Vars(r)["version"]
	if !validVersion(version) {
		writeErrors(w, http.StatusNotFound, "module version not found")
		return
	}

	m, err := h.DB.GetTerraformModule(r.Context(), repo.ID, name, system, version)
	if err != nil {
		h.Log.Errorw("failed to get terraform module", "repo", repo.Name, "module", name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if m == nil {
		writeErrors(w, http.StatusNotFound, "module version not found")
		return
	}

	file := moduleFileName(name, system, version, m.ArchiveExt)

	if h.DenyIfQuarantined(w, r, repo.ID, quarantine.PathKeys(m.ID, "modules/"+name+"/"+system+"/"+version, file, r.URL.Path)...) {
		return
	}

	link, ok := h.mintURL(w, ticket{repoID: repo.ID, kind: kindModule, a: name, b: system, c: version}, file)
	if !ok {
		return
	}

	if err := h.DB.IncrementTerraformModuleDownloadCount(r.Context(), repo.ID, m.ID); err != nil {
		h.Log.Warnw("failed to count terraform module download", "id", m.ID, "error", err)
	}

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Terraform-Get", link)
	w.WriteHeader(http.StatusNoContent)
}

// mintURL builds the ticketed download URL of one file. It writes a 503 and
// returns false when the server has no ticket key.
func (h *HTTPHandler) mintURL(w http.ResponseWriter, t ticket, filename string) (string, bool) {
	if len(h.ticketKey) == 0 {
		h.Log.Errorw("terraform download tickets are disabled: no HMAC key")
		writeErrors(w, http.StatusServiceUnavailable, "download tickets are not available on this server")

		return "", false
	}

	t.expires = h.now().Add(ticketTTL)

	return h.BaseURL + ticketPath + encodeTicket(h.ticketKey, t) + "/" + filename, true
}

// providerType reads and validates {type} from the URL.
func providerType(w http.ResponseWriter, r *http.Request) (string, bool) {
	typ := urlparams.Vars(r)["type"]
	if !validName(typ) {
		writeErrors(w, http.StatusNotFound, "provider not found")
		return "", false
	}

	return typ, true
}

// platformJSON is one platform of a provider version.
type platformJSON struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// providerVersionJSON is one version of the provider versions response.
type providerVersionJSON struct {
	Version   string         `json:"version"`
	Protocols []string       `json:"protocols"`
	Platforms []platformJSON `json:"platforms"`
}

// groupProviderVersions folds platform rows into versions, lowest precedence
// first.
func groupProviderVersions(rows []*models.TerraformProvider) []providerVersionJSON {
	var (
		out   []providerVersionJSON
		index = make(map[string]int)
	)

	for _, p := range rows {
		i, seen := index[p.Version]
		if !seen {
			i = len(out)
			index[p.Version] = i
			out = append(out, providerVersionJSON{Version: p.Version, Protocols: p.Protocols, Platforms: []platformJSON{}})
		}

		out[i].Platforms = append(out[i].Platforms, platformJSON{OS: p.OS, Arch: p.Arch})
	}

	slices.SortStableFunc(out, func(a, b providerVersionJSON) int { return compareVersions(a.Version, b.Version) })

	return out
}

// handleProviderVersions answers GET /{type}/versions.
func (h *HTTPHandler) handleProviderVersions(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	typ, ok := providerType(w, r)
	if !ok {
		return
	}

	stamp, err := h.DB.TerraformProviderStamp(r.Context(), repo.ID, typ)
	if err != nil {
		h.Log.Errorw("failed to stamp terraform provider", "repo", repo.Name, "type", typ, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if emptyStamp(stamp) {
		writeErrors(w, http.StatusNotFound, "provider not found")
		return
	}

	if base.Revalidate(w, r, repo.Public, versionsETag(repo.ID, "provider", typ, stamp), versionsMaxAge) {
		return
	}

	rows, err := h.DB.ListTerraformProviderPlatforms(r.Context(), repo.ID, typ)
	if err != nil {
		h.Log.Errorw("failed to list terraform provider platforms", "repo", repo.Name, "type", typ, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if len(rows) == 0 {
		writeErrors(w, http.StatusNotFound, "provider not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"versions": groupProviderVersions(rows), "warnings": nil})
}
