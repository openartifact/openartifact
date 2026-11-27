// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/httpcache"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/pkg/swift"
)

// artifactMaxAge is how long shared caches may keep a public source archive: it
// never changes under its version.
const artifactMaxAge = 3600

// handleList serves GET /{scope}/{name}: the releases of a package, newest
// first, and the Link header naming the latest release.
func (h *HTTPHandler) handleList(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	p, ok := splitRelease(w, r, swift.KindList)
	if !ok {
		return
	}

	versions, err := h.versions(r, repo, p.Scope, p.Name)
	if err != nil {
		h.internalError(w, "failed to list swift releases", err, "package", swift.ID(p.Scope, p.Name))
		return
	}

	if len(versions) == 0 {
		notFound(w, "package")
		return
	}

	origin := h.PublicBase(w, r)

	body := swift.ListBody(versions, func(v string) string { return releaseURL(origin, repo.Name, p.Scope, p.Name, v) })

	w.Header().Add("Link", swift.Link{
		URL: releaseURL(origin, repo.Name, p.Scope, p.Name, swift.Latest(versions)), Rel: swift.RelLatest,
	}.String())

	writeDoc(w, r, repo, origin, "application/json", body)
}

// versions lists the versions of a package, newest first.
func (h *HTTPHandler) versions(r *http.Request, repo *models.Repository, scope, name string) ([]string, error) {
	rows, err := h.DB.ListSwiftReleaseVersions(r.Context(), repo.ID, scope, name)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller reports the failure
	}

	vs := make([]string, len(rows))
	for i, row := range rows {
		vs[i] = row.Version
	}

	swift.SortNewestFirst(vs)

	return vs, nil
}

// lookup returns the release the request names, or answers and returns nil.
func (h *HTTPHandler) lookup(w http.ResponseWriter, r *http.Request, repo *models.Repository, p swift.Path) *models.SwiftRelease {
	rel, err := h.DB.GetSwiftRelease(r.Context(), repo.ID, p.Scope, p.Name, p.Version)
	if err != nil {
		h.internalError(w, "failed to get swift release", err, "package", swift.ID(p.Scope, p.Name), "version", p.Version)
		return nil
	}

	if rel == nil {
		notFound(w, "release")
		return nil
	}

	return rel
}

// handleRelease serves GET /{scope}/{name}/{version}: the release metadata, and
// /{scope}/{name}/{version}.zip: the source archive.
func (h *HTTPHandler) handleRelease(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	p, ok := splitRelease(w, r, swift.KindRelease, swift.KindArchive)
	if !ok {
		return
	}

	rel := h.lookup(w, r, repo, p)
	if rel == nil {
		return
	}

	if p.Kind == swift.KindArchive {
		h.serveArchive(w, r, repo, rel)
		return
	}

	h.serveRelease(w, r, repo, rel)
}

// serveRelease writes the release document with the links to the neighbouring
// releases.
func (h *HTTPHandler) serveRelease(w http.ResponseWriter, r *http.Request, repo *models.Repository, rel *models.SwiftRelease) {
	versions, err := h.versions(r, repo, rel.Scope, rel.Name)
	if err != nil {
		h.internalError(w, "failed to list swift releases", err, "package", swift.ID(rel.Scope, rel.Name))
		return
	}

	origin := h.PublicBase(w, r)
	link := func(version, relation string) {
		w.Header().Add("Link", swift.Link{
			URL: releaseURL(origin, repo.Name, rel.Scope, rel.Name, version), Rel: relation,
		}.String())
	}

	link(swift.Latest(versions), swift.RelLatest)

	// versions is newest first: the neighbour before this version is the next
	// higher one.
	for i, v := range versions {
		if v != rel.Version {
			continue
		}

		if i > 0 {
			link(versions[i-1], swift.RelSuccessor)
		}

		if i+1 < len(versions) {
			link(versions[i+1], swift.RelPredecessor)
		}
	}

	resource := swift.Resource{Name: swift.SourceArchive, Type: swift.ContentTypeZip, Checksum: rel.SHA256}
	if rel.Signature != "" {
		resource.Signing = &swift.Signing{SignatureBase64Encoded: rel.Signature, SignatureFormat: rel.SignatureFormat}
	}

	metadata := rel.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage("{}")
	}

	body, err := json.Marshal(swift.Release{
		ID:          swift.ID(rel.Scope, rel.Name),
		Version:     rel.Version,
		Resources:   []swift.Resource{resource},
		Metadata:    metadata,
		PublishedAt: rel.CreatedAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		h.internalError(w, "failed to encode swift release", err)
		return
	}

	writeDoc(w, r, repo, origin, "application/json", body)
}

// serveArchive streams the source archive with its Digest and, for a signed
// release, its signature.
func (h *HTTPHandler) serveArchive(w http.ResponseWriter, r *http.Request, repo *models.Repository, rel *models.SwiftRelease) {
	if h.DenyIfQuarantined(w, r, repo.ID, quarantine.PathKeys(rel.ID, rel.Filename, r.URL.Path)...) {
		return
	}

	hd := w.Header()
	hd.Set("Content-Disposition", `attachment; filename="`+rel.Filename+`"`)
	hd.Set("Digest", swift.DigestHeader(rel.SHA256))
	hd.Set("Cache-Control", httpcache.CacheControl(base.SharedCacheable(r, repo.Public), artifactMaxAge))
	hd.Add("Vary", "Authorization")

	if rel.Signature != "" {
		hd.Set("X-Swift-Package-Signature-Format", rel.SignatureFormat)
		hd.Set("X-Swift-Package-Signature", rel.Signature)
	}

	if h.TryPresign(w, r, presign.Blob{
		SHA256: rel.BlobKey, Size: rel.SizeBytes, Filename: rel.Filename, ContentType: swift.ContentTypeZip, Repository: repo,
	}) {
		return
	}

	err := base.ServeBlob(w, r, h.BlobStore, rel.BlobKey, base.ServeBlobOptions{
		ContentType: swift.ContentTypeZip,
		Size:        rel.SizeBytes,
		ETag:        `"` + rel.SHA256 + `"`,
	})
	if err != nil {
		h.internalError(w, "failed to get swift blob", err, "key", rel.BlobKey)
	}
}

// handleManifest serves GET /{scope}/{name}/{version}/Package.swift: the
// manifest, or with ?swift-version=X.Y the version-specific one. A Swift
// version without a manifest of its own is redirected to Package.swift.
func (h *HTTPHandler) handleManifest(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	p, ok := splitRelease(w, r, swift.KindManifest)
	if !ok {
		return
	}

	rel := h.lookup(w, r, repo, p)
	if rel == nil {
		return
	}

	origin := h.PublicBase(w, r)
	manifestURL := releaseURL(origin, repo.Name, rel.Scope, rel.Name, rel.Version) + "/" + swift.ManifestFile

	want := swift.ManifestName(r.URL.Query().Get("swift-version"))

	chosen := findManifest(rel.Manifests, want)
	if chosen == nil && want != swift.ManifestFile {
		w.Header().Set("Location", manifestURL)
		w.WriteHeader(http.StatusSeeOther)

		return
	}

	if chosen == nil {
		notFound(w, "manifest")
		return
	}

	for _, m := range rel.Manifests {
		if m.SwiftVersion == "" {
			continue
		}

		params := map[string]string{"filename": m.Filename}
		if m.ToolsVersion != "" {
			params["swift-tools-version"] = m.ToolsVersion
		}

		w.Header().Add("Link", swift.Link{
			URL: manifestURL + "?swift-version=" + url.QueryEscape(m.SwiftVersion), Rel: swift.RelAlternate, Params: params,
		}.String())
	}

	w.Header().Set("Content-Disposition", `attachment; filename="`+chosen.Filename+`"`)

	writeDoc(w, r, repo, origin, swift.ContentTypeManifest, []byte(chosen.Content))
}

// findManifest returns the manifest with the file name, nil when there is none.
func findManifest(manifests []models.SwiftManifest, filename string) *models.SwiftManifest {
	for i := range manifests {
		if manifests[i].Filename == filename {
			return &manifests[i]
		}
	}

	return nil
}

// handleIdentifiers serves GET /identifiers?url=: the packages published with
// the repository URL, whatever its spelling.
func (h *HTTPHandler) handleIdentifiers(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	raw := r.URL.Query().Get("url")
	if raw == "" {
		swift.WriteProblem(w, http.StatusBadRequest, "the url query parameter is required")
		return
	}

	ids, err := h.DB.ListSwiftIdentifiers(r.Context(), repo.ID, swift.NormalizeURL(raw))
	if err != nil {
		h.internalError(w, "failed to look up swift identifiers", err)
		return
	}

	if len(ids) == 0 {
		notFound(w, "package identifier")
		return
	}

	body, err := json.Marshal(struct {
		Identifiers []string `json:"identifiers"`
	}{ids})
	if err != nil {
		h.internalError(w, "failed to encode swift identifiers", err)
		return
	}

	writeDoc(w, r, repo, "", "application/json", body)
}

// handleLogin serves POST /login: 200 for a request the repository's access
// control let through with credentials, 401 for an anonymous one.
func (h *HTTPHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if h.resolveRepo(w, r) == nil {
		return
	}

	if p := middleware.PrincipalFromContext(r.Context()); p == nil || p.IsAnonymous {
		w.Header().Set("WWW-Authenticate", `Basic realm="`+middleware.AuthRealm+`"`)
		swift.WriteProblem(w, http.StatusUnauthorized, "authentication required")

		return
	}

	w.WriteHeader(http.StatusOK)
}
