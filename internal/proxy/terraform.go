// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/format/terraform"
	"github.com/openartifact/openartifact/internal/httpcache"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/quarantine"
)

// Terraform proxy repositories mirror a Terraform or OpenTofu registry
// (registry.terraform.io, registry.opentofu.org). The repository name is the
// upstream namespace: repository "hashicorp" serves registry.terraform.io's
// hashicorp/* modules and providers under /repos/hashicorp/terraform/, and the
// clients' source addresses carry the host and that namespace.
//
// The registry protocol is answered here, from the same paths the hosted
// registry serves (the group repositories rely on that):
//
//   - The upstream's service discovery document (/.well-known/terraform.json)
//     is cached under ".well-known/terraform.json"; modules.v1 and providers.v1
//     may be relative or absolute and are resolved against it.
//   - Version lists are cached trimmed to what the protocol needs:
//     "modules/<name>/<system>/versions" and "providers/<type>/versions".
//   - A module download answer is cached as {"get": "<source>"} under
//     "modules/<name>/<system>/<version>/download"; a provider package document
//     verbatim under "providers/<type>/<version>/download/<os>/<arch>".
//   - Archives usually live on other hosts (GitHub, vendor release sites). The
//     documents served to clients point at tickets of this server (the ones the
//     hosted registry mints, redeemed through the same credential-free route),
//     and the archive is fetched through the SSRF-safe client when a ticket is
//     redeemed: "modules/<name>/<system>/<version>/<file>" and
//     "providers/<type>/<version>/<os>_<arch>/<file>", together with the
//     provider's SHA256SUMS and signature, which are served untouched like the
//     signing keys.
//
// Integrity: a provider zip must match the package document's shasum and the
// entry the upstream's SHA256SUMS lists for it, else it is refused (502) and
// never stored; it is then immutable. A module archive has no checksum
// upstream: the digest of the first copy is kept and a later fetch (after the
// repository TTL) that differs is refused the same way.
//
// Only github.com repositories and plain archive sources are supported (see
// parseModuleGet); a module with any other git or getter source answers 501. A
// GitHub source is fetched as the codeload tarball of its ref, and a source
// with a wrapper directory or a "//subdir" is repacked (terraform.RepackModuleArchive)
// before it is cached, so the cached archive is what the client extracts.

const (
	tfDiscoveryKey = ".well-known/terraform.json"
	tfJSON         = "application/json"

	// tfVersionsMaxAge is how long shared caches may reuse a version list of a
	// public repository.
	tfVersionsMaxAge = 60

	// tfMaxMessage bounds an upstream-derived string in an error answer.
	tfMaxMessage = 300
)

// refreshTerraformMinter rebuilds the ticket minter after the server URL or
// the secret changed.
func (s *Service) refreshTerraformMinter() {
	s.tfTickets = terraform.NewMinter(s.baseURL, s.tfSecret)
}

// WithTerraformTicketKey sets the server HMAC secret the download tickets of
// Terraform proxy repositories are signed with (the secret the hosted registry
// verifies them with). Without it such repositories answer downloads with 503.
// Call it before serving requests.
func (s *Service) WithTerraformTicketKey(secret []byte) *Service {
	s.tfSecret = secret
	s.refreshTerraformMinter()

	return s
}

// ---- cache keys ----

func tfModuleVersionsKey(name, system string) string {
	return "modules/" + name + "/" + system + "/versions"
}

func tfModuleDownloadKey(name, system, version string) string {
	return "modules/" + name + "/" + system + "/" + version + "/download"
}

func tfModuleArchiveKey(name, system, version, file string) string {
	return "modules/" + name + "/" + system + "/" + version + "/" + file
}

func tfProviderVersionsKey(typ string) string { return "providers/" + typ + "/versions" }

func tfPackageKey(typ, version, osName, arch string) string {
	return "providers/" + typ + "/" + version + "/download/" + osName + "/" + arch
}

func tfZipKey(typ, version, platform, file string) string {
	return "providers/" + typ + "/" + version + "/" + platform + "/" + file
}

func tfProviderFileKey(typ, version, file string) string {
	return "providers/" + typ + "/" + version + "/" + file
}

// ---- answers ----

func writeTFErrors(w http.ResponseWriter, status int, msg string) {
	body, err := json.Marshal(map[string][]string{"errors": {msg}})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	base.WriteJSON(w, status, append(body, '\n'))
}

func writeTFJSON(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	base.WriteJSON(w, http.StatusOK, append(body, '\n'))
}

// writeTFError answers a failed resolve in the registry's error shape.
func (s *Service) writeTFError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errTFUnsupportedSource):
		writeTFErrors(w, http.StatusNotImplemented,
			truncate(err.Error(), tfMaxMessage)+": this proxy fetches modules only from github.com repositories and plain archive URLs "+
				"(zip, tar.gz, ...); see the Terraform format documentation, section proxy repositories")
	case errors.Is(err, errNotFound):
		writeTFErrors(w, http.StatusNotFound, "not found")
	case errors.Is(err, errUpstream):
		writeTFErrors(w, http.StatusBadGateway, "upstream registry error")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.log.Debugw("proxy request abandoned", "error", err)
	default:
		writeTFErrors(w, http.StatusInternalServerError, "internal error")
	}
}

// tfETag derives a validator from the cached blob a rendered document is built
// from and the request-specific parts of the rendering.
func tfETag(blobKey string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(blobKey))

	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}

	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`
}

// ---- upstream access ----

// tfOpts withholds the repository's credentials from every host but the
// upstream's own: archives and checksum files live elsewhere.
func tfOpts(repo *models.Repository, target string, o fetchOpts) fetchOpts {
	o.noAuth = !sameOriginStr(repo.UpstreamURL, target)
	return o
}

// tfReadDoc loads a cached document into memory under the document gate.
func (s *Service) tfReadDoc(ctx context.Context, entry *models.ProxyCacheEntry, limit int64) ([]byte, error) {
	if err := s.packuments.Acquire(ctx); err != nil {
		return nil, err
	}
	defer s.packuments.Release()

	return s.readCachedDoc(ctx, entry, limit)
}

// tfDiscoveryURL is where the upstream publishes its discovery document: the
// well-known path of the upstream host.
func tfDiscoveryURL(upstream string) (*url.URL, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}

	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("upstream url %q is not an http(s) URL", upstream)
	}

	return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/.well-known/terraform.json"}, nil
}

// tfUpstream returns the service roots of the repository's upstream registry,
// fetching its discovery document when it is not cached. A registry without a
// discovery document is a misconfigured upstream, not a missing package.
func (s *Service) tfUpstream(ctx context.Context, repo *models.Repository) (tfEndpoints, error) {
	docURL, err := tfDiscoveryURL(repo.UpstreamURL)
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return tfEndpoints{}, errInternal
	}

	target := docURL.String()

	res, err := s.resolveWith(ctx, repo, tfDiscoveryKey, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchURL(fctx, repo, tfDiscoveryKey, target, tfOpts(repo, target, fetchOpts{
			accept:      tfJSON,
			contentType: tfJSON,
			verify: func(spool io.ReadSeeker) error {
				body, rerr := io.ReadAll(io.LimitReader(spool, tfMaxDoc+1))
				if rerr != nil {
					return rerr
				}

				_, perr := parseTerraformDiscovery(body, docURL)

				return perr
			},
		}))
	})
	if errors.Is(err, errNotFound) {
		s.log.Warnw("proxy upstream has no terraform discovery document", "repo", repo.Name, "url", target)
		return tfEndpoints{}, errUpstream
	}

	if err != nil {
		return tfEndpoints{}, err
	}

	memoKey := res.entry.BlobKey + "\x00" + target
	if ep, ok := s.tfEndpoints.get(memoKey); ok {
		return ep, nil
	}

	body, err := s.tfReadDoc(ctx, res.entry, tfMaxDoc)
	if err != nil {
		return tfEndpoints{}, err
	}

	ep, err := parseTerraformDiscovery(body, docURL)
	if err != nil {
		s.log.Errorw("cached terraform discovery document is unusable", "repo", repo.Name, "error", err)
		return tfEndpoints{}, errInternal
	}

	s.tfEndpoints.put(memoKey, ep)

	return ep, nil
}

// tfCanonTransform adapts a canonicalizing parser to a fetch transform: the
// trimmed document replaces the upstream's. A list without usable versions is a
// package that does not exist; anything unparsable is a failed integrity check.
func (s *Service) tfCanonTransform(canon func(io.Reader) ([]byte, error)) func(*http.Response) error {
	return func(resp *http.Response) error {
		body, err := canon(io.LimitReader(resp.Body, s.maxMeta))

		switch {
		case errors.Is(err, errTFNoVersions):
			return errNotFound
		case err != nil:
			s.log.Warnw("proxy upstream versions document refused", "error", err)
			return errVerify
		}

		replaceBody(resp, body)

		return nil
	}
}

// tfGetDoc is the cached form of a module download answer.
type tfGetDoc struct {
	Get string `json:"get"`
}

// tfModuleGetTransform reads the source a registry names for a module: the
// X-Terraform-Get header of a 204 (registry.terraform.io) or the location of a
// 200 JSON answer (registry.opentofu.org), and caches it as a tfGetDoc.
func (s *Service) tfModuleGetTransform(resp *http.Response) error {
	get := strings.TrimSpace(resp.Header.Get("X-Terraform-Get"))

	if get == "" && resp.StatusCode == http.StatusOK {
		var doc struct {
			Location string `json:"location"`
		}

		if err := json.NewDecoder(io.LimitReader(resp.Body, tfMaxDoc)).Decode(&doc); err != nil {
			s.log.Warnw("proxy upstream module download answer refused", "error", err)
			return errVerify
		}

		get = strings.TrimSpace(doc.Location)
	}

	if get == "" || len(get) > tfMaxGet {
		s.log.Warnw("proxy upstream module download answer names no usable source", "status", resp.StatusCode)
		return errVerify
	}

	body, err := json.Marshal(tfGetDoc{Get: get})
	if err != nil {
		return errInternal
	}

	replaceBody(resp, body)

	return nil
}

// ---- version lists ----

func (s *Service) tfResolveModuleVersions(ctx context.Context, repo *models.Repository, name, system string) (*resolution, error) {
	key := tfModuleVersionsKey(name, system)

	return s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		ep, err := s.tfUpstream(fctx, repo)
		if err != nil {
			return nil, err
		}

		if ep.modules == nil {
			return nil, errNotFound
		}

		target := ep.modules.JoinPath(repo.Name, name, system, "versions").String()

		return s.fetchURL(fctx, repo, key, target, tfOpts(repo, target, fetchOpts{
			accept:      tfJSON,
			contentType: tfJSON,
			transform:   s.tfCanonTransform(canonModuleVersions),
		}))
	})
}

func (s *Service) tfResolveProviderVersions(ctx context.Context, repo *models.Repository, typ string) (*resolution, error) {
	key := tfProviderVersionsKey(typ)

	return s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		ep, err := s.tfUpstream(fctx, repo)
		if err != nil {
			return nil, err
		}

		if ep.providers == nil {
			return nil, errNotFound
		}

		target := ep.providers.JoinPath(repo.Name, typ, "versions").String()

		return s.fetchURL(fctx, repo, key, target, tfOpts(repo, target, fetchOpts{
			accept:      tfJSON,
			contentType: tfJSON,
			transform:   s.tfCanonTransform(canonProviderVersions),
		}))
	})
}

// tfModuleVersions returns the cached, ascending versions of a module.
func (s *Service) tfModuleVersions(ctx context.Context, repo *models.Repository, name, system string) ([]string, *resolution, error) {
	res, err := s.tfResolveModuleVersions(ctx, repo, name, system)
	if err != nil {
		return nil, nil, err
	}

	data, err := s.tfReadDoc(ctx, res.entry, s.maxMeta)
	if err != nil {
		return nil, nil, err
	}

	versions, err := parseCanonModuleVersions(data)
	if err != nil {
		s.log.Errorw("cached terraform module versions are unusable", "repo", repo.Name, "module", name, "error", err)
		return nil, nil, errInternal
	}

	return versions, res, nil
}

// ---- request handlers ----

func (s *Service) serveTerraform(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, ok := parseTerraformPath(relPath)
	if !ok {
		writeTFErrors(w, http.StatusNotFound, "not found")
		return
	}

	// The repository name is the upstream namespace and is sent in URLs.
	if !terraform.ValidName(repo.Name) {
		writeTFErrors(w, http.StatusNotFound, "the repository name is not a valid registry namespace")
		return
	}

	switch p.kind {
	case tfProviderVersions:
		s.serveTFProviderVersions(w, r, repo, p)
	case tfProviderPackage:
		s.serveTFProviderPackage(w, r, repo, p)
	case tfModuleVersions:
		s.serveTFModuleVersions(w, r, repo, p)
	case tfModuleLatest, tfModuleLatestDownload, tfModuleVersion, tfModuleDownload:
		s.serveTFModule(w, r, repo, p)
	}
}

func (s *Service) serveTFProviderVersions(w http.ResponseWriter, r *http.Request, repo *models.Repository, p tfPath) {
	key := tfProviderVersionsKey(p.name)
	resolve := func() (*resolution, error) { return s.tfResolveProviderVersions(r.Context(), repo, p.name) }

	res, err := resolve()
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	h := w.Header()
	h.Set("Cache-Control", httpcache.CacheControl(base.SharedCacheable(r, repo.Public), tfVersionsMaxAge))
	h.Add("Vary", "Authorization")

	s.serveResolved(w, r, repo, key, res, resolve)
}

// tfModuleVersionsOut is the module version list as clients get it.
type tfModuleVersionsOut struct {
	Modules []struct {
		Source   string      `json:"source"`
		Versions []tfVersion `json:"versions"`
	} `json:"modules"`
}

func (s *Service) serveTFModuleVersions(w http.ResponseWriter, r *http.Request, repo *models.Repository, p tfPath) {
	versions, res, err := s.tfModuleVersions(r.Context(), repo, p.name, p.system)
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	if res.stale {
		w.Header().Set("Warning", staleWarning)
	}

	if base.Revalidate(w, r, repo.Public, tfETag(res.entry.BlobKey, repo.Name), tfVersionsMaxAge) {
		return
	}

	var out tfModuleVersionsOut

	out.Modules = make([]struct {
		Source   string      `json:"source"`
		Versions []tfVersion `json:"versions"`
	}, 1)
	out.Modules[0].Source = strings.Join([]string{repo.Name, p.name, p.system}, "/")

	for _, v := range versions {
		out.Modules[0].Versions = append(out.Modules[0].Versions, tfVersion{Version: v})
	}

	writeTFJSON(w, out)
}

// tfModuleDetails is the module registry's description of one version; the
// fields the upstream's list does not carry are left out.
type tfModuleDetails struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Downloads   int64  `json:"downloads"`
	Verified    bool   `json:"verified"`
}

// serveTFModule answers the module requests that name (or default to) a
// version: details, the download redirect of the latest version and the
// download call itself.
func (s *Service) serveTFModule(w http.ResponseWriter, r *http.Request, repo *models.Repository, p tfPath) {
	if p.kind == tfModuleDownload {
		s.serveTFModuleDownload(w, r, repo, p)
		return
	}

	versions, res, err := s.tfModuleVersions(r.Context(), repo, p.name, p.system)
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	version := p.version
	if version == "" {
		version = terraform.LatestVersion(versions)
	}

	if version == "" || !slices.Contains(versions, version) {
		writeTFErrors(w, http.StatusNotFound, "module version not found")
		return
	}

	if res.stale {
		w.Header().Set("Warning", staleWarning)
	}

	if p.kind == tfModuleLatestDownload {
		// The coordinates are validated names and a listed version; the host is
		// the configured base path: a plain 302 with a relative Location.
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("Location", terraform.PathPrefix(s.baseURL)+terraform.RegistryBasePath(repo.Name)+"/"+p.name+"/"+p.system+"/"+version+"/download")
		w.WriteHeader(http.StatusFound)

		return
	}

	w.Header().Set("Cache-Control", "private, no-cache")
	writeTFJSON(w, tfModuleDetails{
		ID:        strings.Join([]string{repo.Name, p.name, p.system, version}, "/"),
		Namespace: repo.Name,
		Name:      p.name,
		Provider:  p.system,
		Version:   version,
	})
}

// tfModuleSource returns the archive source of a module version: the cached
// download answer of the upstream, interpreted by parseModuleGet.
func (s *Service) tfModuleSource(ctx context.Context, repo *models.Repository, name, system, version string) (tfModuleSource, error) {
	key := tfModuleDownloadKey(name, system, version)

	res, err := s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		ep, uerr := s.tfUpstream(fctx, repo)
		if uerr != nil {
			return nil, uerr
		}

		if ep.modules == nil {
			return nil, errNotFound
		}

		target := ep.modules.JoinPath(repo.Name, name, system, version, "download").String()

		return s.fetchURL(fctx, repo, key, target, tfOpts(repo, target, fetchOpts{
			accept:      tfJSON,
			contentType: tfJSON,
			transform:   s.tfModuleGetTransform,
		}))
	})
	if err != nil {
		return tfModuleSource{}, err
	}

	ep, err := s.tfUpstream(ctx, repo)
	if err != nil {
		return tfModuleSource{}, err
	}

	data, err := s.tfReadDoc(ctx, res.entry, tfMaxDoc)
	if err != nil {
		return tfModuleSource{}, err
	}

	var doc tfGetDoc
	if err = json.Unmarshal(data, &doc); err != nil {
		s.log.Errorw("cached terraform module download answer is unusable", "repo", repo.Name, "module", name, "error", err)
		return tfModuleSource{}, errInternal
	}

	src, err := parseModuleGet(doc.Get, ep.modules.JoinPath(repo.Name, name, system, version, "download"), cmp.Or(s.tfCodeload, tfCodeloadBase))
	if err != nil {
		if errors.Is(err, errTFUnsupportedSource) {
			s.log.Infow("terraform module source is not a plain archive URL", "repo", repo.Name, "module", name, "version", version, "error", err)
			return tfModuleSource{}, err
		}

		s.log.Warnw("proxy upstream module source refused", "repo", repo.Name, "module", name, "version", version, "error", err)

		return tfModuleSource{}, errVerify
	}

	return src, nil
}

// tfMinter returns the ticket minter, nil when tickets are not configured.
func (s *Service) tfMinter() *terraform.Minter {
	if s.tfTickets == nil || !s.tfTickets.Enabled() {
		return nil
	}

	return s.tfTickets
}

// serveTFModuleDownload answers GET /{mod}/{system}/{version}/download: 204 with
// X-Terraform-Get naming the ticketed archive URL of this server.
func (s *Service) serveTFModuleDownload(w http.ResponseWriter, r *http.Request, repo *models.Repository, p tfPath) {
	minter := s.tfMinter()
	if minter == nil {
		writeTFErrors(w, http.StatusServiceUnavailable, "downloads are not configured on this server")
		return
	}

	src, err := s.tfModuleSource(r.Context(), repo, p.name, p.system, p.version)
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	file := terraform.ModuleFileName(p.name, p.system, p.version, src.ext)

	key := tfModuleArchiveKey(p.name, p.system, p.version, file)
	if quarantine.Enforce(w, r, s.db, repo.ID, quarantine.PathKeys(key)...) {
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Terraform-Get", minter.URL(repo.ID, terraform.DownloadModule, p.name, p.system, p.version, file))
	w.WriteHeader(http.StatusNoContent)
}

// tfPackage returns a provider package document: the upstream's, as cached,
// with its URLs absolute.
func (s *Service) tfPackage(ctx context.Context, repo *models.Repository, typ, version, osName, arch string) (*tfPackageDoc, error) {
	key := tfPackageKey(typ, version, osName, arch)

	packageURL := func(ep tfEndpoints) *url.URL {
		return ep.providers.JoinPath(repo.Name, typ, version, "download", osName, arch)
	}

	res, err := s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		ep, uerr := s.tfUpstream(fctx, repo)
		if uerr != nil {
			return nil, uerr
		}

		if ep.providers == nil {
			return nil, errNotFound
		}

		docURL := packageURL(ep)
		target := docURL.String()

		return s.fetchURL(fctx, repo, key, target, tfOpts(repo, target, fetchOpts{
			accept:      tfJSON,
			contentType: tfJSON,
			verify: func(spool io.ReadSeeker) error {
				body, rerr := io.ReadAll(io.LimitReader(spool, tfMaxDoc+1))
				if rerr != nil {
					return rerr
				}

				_, perr := parseProviderPackage(body, docURL, osName, arch)

				return perr
			},
		}))
	})
	if err != nil {
		return nil, err
	}

	ep, err := s.tfUpstream(ctx, repo)
	if err != nil {
		return nil, err
	}

	data, err := s.tfReadDoc(ctx, res.entry, tfMaxDoc)
	if err != nil {
		return nil, err
	}

	doc, err := parseProviderPackage(data, packageURL(ep), osName, arch)
	if err != nil {
		s.log.Errorw("cached terraform provider package document is unusable", "repo", repo.Name, "provider", typ, "error", err)
		return nil, errInternal
	}

	return doc, nil
}

// tfSplitPlatform splits "os_arch" (a validated ticket coordinate).
func tfSplitPlatform(platform string) (osName, arch string, ok bool) {
	osName, arch, ok = strings.Cut(platform, "_")

	return osName, arch, ok && terraform.ValidPlatformPart(osName) && terraform.ValidPlatformPart(arch)
}

// serveTFProviderPackage answers GET /{type}/{version}/download/{os}/{arch}: the
// upstream's package document with the three file URLs replaced by tickets of
// this server. Checksum, file name, protocols and signing keys are the
// upstream's own.
func (s *Service) serveTFProviderPackage(w http.ResponseWriter, r *http.Request, repo *models.Repository, p tfPath) {
	minter := s.tfMinter()
	if minter == nil {
		writeTFErrors(w, http.StatusServiceUnavailable, "downloads are not configured on this server")
		return
	}

	doc, err := s.tfPackage(r.Context(), repo, p.name, p.version, p.os, p.arch)
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	platform := p.os + "_" + p.arch

	zipKey := tfZipKey(p.name, p.version, platform, doc.Filename)
	if quarantine.Enforce(w, r, s.db, repo.ID, quarantine.PathKeys(zipKey)...) {
		return
	}

	sumsName, sigName := urlBase(doc.ShasumsURL), urlBase(doc.ShasumsSignatureURL)

	s.tfDropChangedZip(r.Context(), repo, zipKey, doc.Shasum,
		tfProviderFileKey(p.name, p.version, sumsName), tfProviderFileKey(p.name, p.version, sigName))

	out := *doc
	out.DownloadURL = minter.URL(repo.ID, terraform.DownloadProviderZip, p.name, p.version, platform, doc.Filename)
	out.ShasumsURL = minter.URL(repo.ID, terraform.DownloadProviderSums, p.name, p.version, platform, sumsName)
	out.ShasumsSignatureURL = minter.URL(repo.ID, terraform.DownloadProviderSumsSig, p.name, p.version, platform, sigName)

	w.Header().Set("Cache-Control", "private, no-store")
	writeTFJSON(w, out)
}

// tfDropChangedZip forgets a cached provider zip whose digest is no longer the
// one the upstream's package document announces: the upstream republished the
// version, and the new file is verified and fetched on the next download. The
// cached checksum file and its signature (otherKeys) describe the old file and
// go with it.
func (s *Service) tfDropChangedZip(ctx context.Context, repo *models.Repository, zipKey, shasum string, otherKeys ...string) {
	entry, err := s.db.GetProxyCacheEntry(ctx, repo.ID, zipKey)
	if err != nil {
		s.log.Errorw("failed to read proxy cache entry", "repo", repo.Name, "path", zipKey, "error", err)
		return
	}

	if entry == nil || entry.BlobKey == "" || strings.EqualFold(entry.BlobKey, shasum) {
		return
	}

	s.log.Warnw("terraform provider package changed upstream, dropping the cached copy",
		"repo", repo.Name, "path", zipKey, "cached_sha256", entry.BlobKey, "upstream_sha256", shasum)

	for _, key := range append([]string{zipKey}, otherKeys...) {
		if err := s.db.DeleteProxyCacheEntry(ctx, repo.ID, key); err != nil {
			s.log.Errorw("failed to drop proxy cache entry", "repo", repo.Name, "path", key, "error", err)
		}
	}
}

// ---- ticket downloads ----

// ServeTerraformDownload serves the file a ticket of proxy repo names, fetching
// it from its upstream host first when it is not cached (terraform.ProxyServer).
// The ticket's coordinates were validated by the caller; the requested file
// name must be the one the documents served by this proxy carry.
func (s *Service) ServeTerraformDownload(w http.ResponseWriter, r *http.Request, repo *models.Repository, d terraform.Download) {
	if repo.Blocked {
		http.Error(w, "repository is blocked", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "proxy repositories are read-only", http.StatusMethodNotAllowed)

		return
	}

	if repo.Format != "terraform" {
		http.NotFound(w, r)
		return
	}

	switch d.Kind {
	case terraform.DownloadModule:
		s.serveTFModuleArchive(w, r, repo, d)
	case terraform.DownloadProviderZip:
		s.serveTFProviderZip(w, r, repo, d)
	case terraform.DownloadProviderSums, terraform.DownloadProviderSumsSig:
		s.serveTFProviderFile(w, r, repo, d)
	default:
		writeTFErrors(w, http.StatusNotFound, "not found")
	}
}

func (s *Service) serveTFModuleArchive(w http.ResponseWriter, r *http.Request, repo *models.Repository, d terraform.Download) {
	src, err := s.tfModuleSource(r.Context(), repo, d.A, d.B, d.C)
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	if file := terraform.ModuleFileName(d.A, d.B, d.C, src.ext); file != d.File {
		writeTFErrors(w, http.StatusNotFound, "not found")
		return
	}

	key := tfModuleArchiveKey(d.A, d.B, d.C, d.File)
	resolve := func() (*resolution, error) {
		return s.resolveWith(r.Context(), repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
			return s.fetchTFModuleArchive(fctx, repo, key, src)
		})
	}

	res, err := resolve()
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

// fetchTFModuleArchive downloads a module archive from its source host. The
// registry publishes no checksum, so the digest of the copy cached before is
// the reference: an archive that changed under the same version is refused
// (errVerify, logged by store) and the version stays as it was until an admin
// purges the entry.
func (s *Service) fetchTFModuleArchive(ctx context.Context, repo *models.Repository, key string, src tfModuleSource) (*models.ProxyCacheEntry, error) {
	o := fetchOpts{
		contentType: archiveContentType(src.ext),
		verify:      func(spool io.ReadSeeker) error { return verifyTFArchive(src.ext, spool) },
	}

	prev, err := s.db.GetProxyCacheEntry(ctx, repo.ID, key)
	if err != nil {
		s.log.Errorw("failed to read proxy cache entry", "repo", repo.Name, "path", key, "error", err)
		return nil, errInternal
	}

	if prev != nil && prev.StatusCode == http.StatusOK && prev.BlobKey != "" {
		o.wantSHA256 = prev.BlobKey
	}

	if src.repack() {
		// The digest that is pinned is the one of the repacked bytes.
		o.transform = func(resp *http.Response) error { return s.repackTFModule(repo, key, src, resp) }
	}

	return s.fetchURL(ctx, repo, key, src.url, tfOpts(repo, src.url, o))
}

// tfSpoolFile is a temporary file that is removed when it is closed.
type tfSpoolFile struct{ *os.File }

func (f tfSpoolFile) Close() error {
	err := f.File.Close()
	if rerr := os.Remove(f.Name()); rerr != nil && err == nil {
		err = rerr
	}

	return err
}

// newTFSpool creates an anonymous temporary file of the proxy's kind.
func (s *Service) newTFSpool() (tfSpoolFile, error) {
	f, err := os.CreateTemp("", "openartifact-proxy-*")
	if err != nil {
		s.log.Errorw("failed to create proxy spool file", "error", err)
		return tfSpoolFile{}, errInternal
	}

	return tfSpoolFile{f}, nil
}

// repackTFModule replaces the body of a module archive answer by the repacked
// archive (wrapper directory and subdirectory handled as src says). The
// upstream body is spooled first, under the proxy's size limit, so a network
// failure is an upstream failure and the archive can be read at random; the
// repacked file becomes resp.Body and is removed when fetchURL closes it.
func (s *Service) repackTFModule(repo *models.Repository, key string, src tfModuleSource, resp *http.Response) error {
	if resp.ContentLength > s.maxBody {
		s.log.Warnw("proxy upstream file exceeds size limit", "repo", repo.Name, "path", key, "size", resp.ContentLength)
		return errUpstream
	}

	in, err := s.newTFSpool()
	if err != nil {
		return err
	}
	defer func() { base.CloseLog(in, s.log) }()

	size, err := iocopy.Copy(in, io.LimitReader(resp.Body, s.maxBody+1))
	switch {
	case err != nil:
		s.log.Warnw("proxy upstream body read failed", "repo", repo.Name, "path", key, "error", err)
		return errUpstreamDown
	case size > s.maxBody:
		s.log.Warnw("proxy upstream body oversized", "repo", repo.Name, "path", key, "size", size)
		return errUpstream
	case resp.ContentLength >= 0 && size != resp.ContentLength:
		s.log.Warnw("proxy upstream body truncated", "repo", repo.Name, "path", key, "size", size)
		return errUpstreamDown
	}

	if _, err := in.Seek(0, io.SeekStart); err != nil {
		s.log.Errorw("failed to rewind proxy spool file", "error", err)
		return errInternal
	}

	if verr := verifyTFArchive(src.ext, in); verr != nil {
		s.log.Warnw("proxy upstream file failed verification, refusing", "repo", repo.Name, "path", key, "error", verr)
		return errVerify
	}

	out, err := s.newTFSpool()
	if err != nil {
		return err
	}

	if rerr := terraform.RepackModuleArchive(out, in, size, src.strip, src.subdir); rerr != nil {
		base.CloseLog(out, s.log)

		if terraform.IsRepackError(rerr) {
			s.log.Warnw("proxy upstream module archive refused", "repo", repo.Name, "path", key, "url", src.url, "subdir", src.subdir, "error", rerr)
			return errVerify
		}

		s.log.Errorw("failed to repack module archive", "repo", repo.Name, "path", key, "error", rerr)

		return errInternal
	}

	outSize, err := out.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = out.Seek(0, io.SeekStart)
	}

	if err != nil {
		base.CloseLog(out, s.log)
		s.log.Errorw("failed to rewind proxy spool file", "error", err)

		return errInternal
	}

	resp.Body = out
	resp.ContentLength = outSize
	resp.TransferEncoding = nil

	return nil
}

func (s *Service) serveTFProviderZip(w http.ResponseWriter, r *http.Request, repo *models.Repository, d terraform.Download) {
	osName, arch, ok := tfSplitPlatform(d.C)
	if !ok {
		writeTFErrors(w, http.StatusNotFound, "not found")
		return
	}

	doc, err := s.tfPackage(r.Context(), repo, d.A, d.B, osName, arch)
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	if doc.Filename != d.File {
		writeTFErrors(w, http.StatusNotFound, "not found")
		return
	}

	key := tfZipKey(d.A, d.B, d.C, d.File)
	resolve := func() (*resolution, error) {
		return s.resolveWith(r.Context(), repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
			return s.fetchTFProviderZip(fctx, repo, key, d.A, d.B, doc)
		})
	}

	res, err := resolve()
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

// fetchTFProviderZip downloads a provider zip and admits it only when its
// digest is the one the package document announces and the one the upstream's
// SHA256SUMS lists for its file name.
func (s *Service) fetchTFProviderZip(ctx context.Context, repo *models.Repository, key, typ, version string, doc *tfPackageDoc) (*models.ProxyCacheEntry, error) {
	sums, err := s.tfProviderSums(ctx, repo, typ, version, doc)
	if err != nil {
		return nil, err
	}

	if listed := sums[doc.Filename]; listed != doc.Shasum {
		s.log.Warnw("terraform provider checksum file does not vouch for the package, refusing",
			"repo", repo.Name, "provider", typ, "version", version, "file", doc.Filename, "package_sha256", doc.Shasum, "listed_sha256", listed)

		return nil, errVerify
	}

	return s.fetchURL(ctx, repo, key, doc.DownloadURL, tfOpts(repo, doc.DownloadURL, fetchOpts{
		wantSHA256:  doc.Shasum,
		contentType: "application/zip",
		verify:      func(spool io.ReadSeeker) error { return verifyTFArchive("zip", spool) },
	}))
}

// tfResolveProviderFile resolves the cache entry of a provider's checksum file
// or signature, fetched from target.
func (s *Service) tfResolveProviderFile(ctx context.Context, repo *models.Repository, key, target, contentType string, verify func(io.ReadSeeker) error) (*resolution, error) {
	return s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchURL(fctx, repo, key, target, tfOpts(repo, target, fetchOpts{contentType: contentType, verify: verify}))
	})
}

func verifyTFSums(spool io.ReadSeeker) error {
	data, err := io.ReadAll(io.LimitReader(spool, tfMaxSums+1))
	if err != nil {
		return err
	}

	_, err = parseSHA256SUMS(data)

	return err
}

// verifyTFSignature accepts what a detached OpenPGP signature looks like: a
// binary packet (the high bit of its tag is set) or an armored block.
func verifyTFSignature(spool io.ReadSeeker) error {
	data, err := io.ReadAll(io.LimitReader(spool, 1<<16+1))
	if err != nil {
		return err
	}

	switch {
	case len(data) == 0 || len(data) > 1<<16:
		return errors.New("signature has an unusable size")
	case data[0]&0x80 != 0, strings.HasPrefix(string(data), "-----BEGIN PGP"):
		return nil
	}

	return errors.New("not a PGP signature")
}

// tfProviderSums returns the digests the upstream's SHA256SUMS lists, fetching
// the file (cached for the repository TTL) when needed.
func (s *Service) tfProviderSums(ctx context.Context, repo *models.Repository, typ, version string, doc *tfPackageDoc) (map[string]string, error) {
	key := tfProviderFileKey(typ, version, urlBase(doc.ShasumsURL))

	res, err := s.tfResolveProviderFile(ctx, repo, key, doc.ShasumsURL, "text/plain; charset=utf-8", verifyTFSums)
	if err != nil {
		return nil, err
	}

	data, err := s.tfReadDoc(ctx, res.entry, tfMaxSums)
	if err != nil {
		return nil, err
	}

	sums, err := parseSHA256SUMS(data)
	if err != nil {
		s.log.Errorw("cached terraform checksum file is unusable", "repo", repo.Name, "provider", typ, "error", err)
		return nil, errInternal
	}

	return sums, nil
}

// serveTFProviderFile serves the SHA256SUMS file or its signature, untouched.
func (s *Service) serveTFProviderFile(w http.ResponseWriter, r *http.Request, repo *models.Repository, d terraform.Download) {
	osName, arch, ok := tfSplitPlatform(d.C)
	if !ok {
		// Tickets of hosted repositories name no platform; a proxy needs one
		// to find the package document that tells where the file is.
		writeTFErrors(w, http.StatusNotFound, "not found")
		return
	}

	doc, err := s.tfPackage(r.Context(), repo, d.A, d.B, osName, arch)
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	target, contentType, verify := doc.ShasumsURL, "text/plain; charset=utf-8", verifyTFSums
	if d.Kind == terraform.DownloadProviderSumsSig {
		target, contentType, verify = doc.ShasumsSignatureURL, "application/pgp-signature", verifyTFSignature
	}

	if urlBase(target) != d.File {
		writeTFErrors(w, http.StatusNotFound, "not found")
		return
	}

	key := tfProviderFileKey(d.A, d.B, d.File)
	resolve := func() (*resolution, error) {
		return s.tfResolveProviderFile(r.Context(), repo, key, target, contentType, verify)
	}

	res, err := resolve()
	if err != nil {
		s.writeTFError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}
