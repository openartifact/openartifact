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
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/publicurl"
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
	s.tfTickets = terraform.NewMinter(publicurl.PathPrefix(s.baseURL), s.tfSecret)
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
func (s *Service) writeTFError(w http.ResponseWriter, err error) {
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

// tfDocAt makes d a document whose URL the upstream's discovery document
// decides: at picks it from the service roots (nil when the upstream offers no
// such service, which is a missing document) and opts says how it is checked
// (given the URL). The discovery lookup runs inside the fetch flight, so a
// cached document never needs it.
func (s *Service) tfDocAt(repo *models.Repository, d metaDoc, at func(tfEndpoints) *url.URL, opts func(target string) fetchOpts) metaDoc {
	d.locate = func(fctx context.Context) (string, error) {
		ep, err := s.tfUpstream(fctx, repo)
		if err != nil {
			return "", err
		}

		u := at(ep)
		if u == nil {
			return "", errNotFound
		}

		return u.String(), nil
	}
	d.opts = func(_ context.Context, target string) fetchOpts { return tfOpts(repo, target, opts(target)) }

	return d
}

// serveTFCached answers a request for the cached file key like the generic
// cached-file answer, with the registry's error shape.
func (s *Service) serveTFCached(w http.ResponseWriter, r *http.Request, repo *models.Repository, key string, fetch fetchFunc) {
	resolve := func() (*resolution, error) { return s.resolveWith(r.Context(), repo, key, fetch) }

	res, err := resolve()
	if err != nil {
		s.writeTFError(w, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

// tfFileDoc is a file served untouched whose location the cached package
// document names (a provider's checksum file or signature); like every
// download it only gets the repository's credentials from the upstream's own
// origin.
func tfFileDoc(repo *models.Repository, key, target, what string, o fetchOpts) metaDoc {
	return metaDoc{
		key:   key,
		url:   func(string) (string, error) { return target, nil },
		opts:  staticOpts(tfOpts(repo, target, o)),
		limit: tfMaxSums,
		what:  what,
	}
}

// tfSumsOpts checks a provider's SHA256SUMS file.
var tfSumsOpts = fetchOpts{contentType: "text/plain; charset=utf-8", verify: verifyTFSums} //nolint:gochecknoglobals // read-only table

var (
	// errTFBadUpstreamURL marks an upstream_url that is not an http(s) URL.
	errTFBadUpstreamURL = errors.New("unusable upstream url")
	// errTFBadSignature marks a signature file that is not a detached PGP signature.
	errTFBadSignature = errors.New("unusable signature file")
)

// tfDiscoveryURL is where the upstream publishes its discovery document: the
// well-known path of the upstream host.
func tfDiscoveryURL(upstream string) (*url.URL, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}

	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: %q is not an http(s) URL", errTFBadUpstreamURL, upstream)
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

	d := metaDoc{
		key: tfDiscoveryKey,
		url: func(string) (string, error) { return target, nil },
		opts: staticOpts(tfOpts(repo, target, fetchOpts{
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
		})),
		limit: tfMaxDoc,
		what:  "terraform discovery document",
	}

	res, err := s.resolveDoc(ctx, repo, d)
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

	_, body, release, err := s.loadDoc(ctx, repo, d, res)
	if err != nil {
		return tfEndpoints{}, err
	}

	ep, err := parseTerraformDiscovery(body, docURL)

	release()

	if err != nil {
		return tfEndpoints{}, s.unusableDoc(ctx, repo, d, err)
	}

	s.tfEndpoints.put(memoKey, ep)

	return ep, nil
}

// tfModuleVersionsTransform and tfProviderVersionsTransform are fetch
// transforms: the trimmed version list replaces the upstream's document.
func (s *Service) tfModuleVersionsTransform(resp *http.Response) error {
	return s.tfCanonize(resp, canonModuleVersions)
}

func (s *Service) tfProviderVersionsTransform(resp *http.Response) error {
	return s.tfCanonize(resp, canonProviderVersions)
}

// tfCanonize runs a canonicalizing parser over the body. A list without usable
// versions is a package that does not exist; anything unparsable is a failed
// integrity check.
func (s *Service) tfCanonize(resp *http.Response, canon func(io.Reader) ([]byte, error)) error {
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

func (s *Service) tfModuleVersionsDoc(repo *models.Repository, name, system string) metaDoc {
	d := metaDoc{key: tfModuleVersionsKey(name, system), limit: int(s.maxMeta), what: "terraform module versions"}

	return s.tfDocAt(repo, d,
		func(ep tfEndpoints) *url.URL {
			if ep.modules == nil {
				return nil
			}

			return ep.modules.JoinPath(repo.Name, name, system, "versions")
		},
		func(string) fetchOpts {
			return fetchOpts{accept: tfJSON, contentType: tfJSON, transform: s.tfModuleVersionsTransform}
		})
}

func (s *Service) tfProviderVersionsDoc(repo *models.Repository, typ string) metaDoc {
	d := metaDoc{key: tfProviderVersionsKey(typ), limit: int(s.maxMeta), what: "terraform provider versions"}

	return s.tfDocAt(repo, d,
		func(ep tfEndpoints) *url.URL {
			if ep.providers == nil {
				return nil
			}

			return ep.providers.JoinPath(repo.Name, typ, "versions")
		},
		func(string) fetchOpts {
			return fetchOpts{accept: tfJSON, contentType: tfJSON, transform: s.tfProviderVersionsTransform}
		})
}

// tfModuleVersions returns the cached, ascending versions of a module.
func (s *Service) tfModuleVersions(ctx context.Context, repo *models.Repository, name, system string) ([]string, *resolution, error) {
	return parseDoc(ctx, s, repo, s.tfModuleVersionsDoc(repo, name, system), func(body []byte, _ *resolution) ([]string, error) {
		return parseCanonModuleVersions(body)
	})
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
	d := s.tfProviderVersionsDoc(repo, p.name)
	resolve := func() (*resolution, error) { return s.resolveDoc(r.Context(), repo, d) }

	res, err := resolve()
	if err != nil {
		s.writeTFError(w, err)
		return
	}

	h := w.Header()
	h.Set("Cache-Control", httpcache.CacheControl(base.SharedCacheable(r, repo.Public), tfVersionsMaxAge))
	h.Add("Vary", "Authorization")

	s.serveResolved(w, r, repo, d.key, res, resolve)
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
		s.writeTFError(w, err)
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
		s.writeTFError(w, err)
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
		prefix := publicurl.PathPrefix(s.baseURL) + terraform.RegistryBasePath(repo.Name)
		w.Header().Set("Location", prefix+"/"+p.name+"/"+p.system+"/"+version+"/download")
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
	d := metaDoc{key: tfModuleDownloadKey(name, system, version), limit: tfMaxDoc, what: "terraform module download answer"}

	downloadURL := func(ep tfEndpoints) *url.URL {
		if ep.modules == nil {
			return nil
		}

		return ep.modules.JoinPath(repo.Name, name, system, version, "download")
	}

	d = s.tfDocAt(repo, d, downloadURL, func(string) fetchOpts {
		return fetchOpts{accept: tfJSON, contentType: tfJSON, transform: s.tfModuleGetTransform}
	})

	ep, err := s.tfUpstream(ctx, repo)
	if err != nil {
		return tfModuleSource{}, err
	}

	if downloadURL(ep) == nil {
		return tfModuleSource{}, errNotFound
	}

	doc, _, err := parseDoc(ctx, s, repo, d, func(body []byte, _ *resolution) (tfGetDoc, error) {
		var g tfGetDoc

		return g, json.Unmarshal(body, &g)
	})
	if err != nil {
		return tfModuleSource{}, err
	}

	src, err := parseModuleGet(
		doc.Get,
		downloadURL(ep),
		cmp.Or(s.tfCodeload, tfCodeloadBase),
	)
	if err != nil {
		if errors.Is(err, errTFUnsupportedSource) {
			s.log.Infow(
				"terraform module source is not a plain archive URL",
				"repo",
				repo.Name,
				"module",
				name,
				"version",
				version,
				"error",
				err,
			)
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
		s.writeTFError(w, err)
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
	packageURL := func(ep tfEndpoints) *url.URL {
		if ep.providers == nil {
			return nil
		}

		return ep.providers.JoinPath(repo.Name, typ, version, "download", osName, arch)
	}

	d := metaDoc{key: tfPackageKey(typ, version, osName, arch), limit: tfMaxDoc, what: "terraform provider package document"}

	d = s.tfDocAt(repo, d, packageURL, func(target string) fetchOpts {
		return fetchOpts{
			accept:      tfJSON,
			contentType: tfJSON,
			verify: func(spool io.ReadSeeker) error {
				body, rerr := io.ReadAll(io.LimitReader(spool, tfMaxDoc+1))
				if rerr != nil {
					return rerr
				}

				docURL, perr := url.Parse(target)
				if perr != nil {
					return perr
				}

				_, perr = parseProviderPackage(body, docURL, osName, arch)

				return perr
			},
		}
	})

	ep, err := s.tfUpstream(ctx, repo)
	if err != nil {
		return nil, err
	}

	if packageURL(ep) == nil {
		return nil, errNotFound
	}

	doc, _, err := parseDoc(ctx, s, repo, d, func(body []byte, _ *resolution) (*tfPackageDoc, error) {
		return parseProviderPackage(body, packageURL(ep), osName, arch)
	})

	return doc, err
}

// tfSplitPlatform splits "os_arch" (a validated ticket coordinate).
func tfSplitPlatform(platform string) (string, string, bool) {
	osName, arch, ok := strings.Cut(platform, "_")

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
		s.writeTFError(w, err)
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
		s.writeTFError(w, err)
		return
	}

	if file := terraform.ModuleFileName(d.A, d.B, d.C, src.ext); file != d.File {
		writeTFErrors(w, http.StatusNotFound, "not found")
		return
	}

	key := tfModuleArchiveKey(d.A, d.B, d.C, d.File)

	s.serveTFCached(w, r, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchTFModuleArchive(fctx, repo, key, src)
	})
}

// fetchTFModuleArchive downloads a module archive from its source host. The
// registry publishes no checksum, so the digest of the copy cached before is
// the reference: an archive that changed under the same version is refused
// (errVerify, logged by store) and the version stays as it was until an admin
// purges the entry.
func (s *Service) fetchTFModuleArchive(
	ctx context.Context,
	repo *models.Repository,
	key string,
	src tfModuleSource,
) (*models.ProxyCacheEntry, error) {
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

	return s.fetchDownload(ctx, repo, key, download{url: src.url, opts: o})
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
			s.log.Warnw(
				"proxy upstream module archive refused",
				"repo",
				repo.Name,
				"path",
				key,
				"url",
				src.url,
				"subdir",
				src.subdir,
				"error",
				rerr,
			)
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
		s.writeTFError(w, err)
		return
	}

	if doc.Filename != d.File {
		writeTFErrors(w, http.StatusNotFound, "not found")
		return
	}

	key := tfZipKey(d.A, d.B, d.C, d.File)

	s.serveTFCached(w, r, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchTFProviderZip(fctx, repo, key, d.A, d.B, doc)
	})
}

// fetchTFProviderZip downloads a provider zip and admits it only when its
// digest is the one the package document announces and the one the upstream's
// SHA256SUMS lists for its file name.
func (s *Service) fetchTFProviderZip(
	ctx context.Context,
	repo *models.Repository,
	key, typ, version string,
	doc *tfPackageDoc,
) (*models.ProxyCacheEntry, error) {
	sums, err := s.tfProviderSums(ctx, repo, typ, version, doc)
	if err != nil {
		return nil, err
	}

	if listed := sums[doc.Filename]; listed != doc.Shasum {
		s.log.Warnw(
			"terraform provider checksum file does not vouch for the package, refusing",
			"repo",
			repo.Name,
			"provider",
			typ,
			"version",
			version,
			"file",
			doc.Filename,
			"package_sha256",
			doc.Shasum,
			"listed_sha256",
			listed,
		)

		return nil, errVerify
	}

	return s.fetchDownload(ctx, repo, key, download{url: doc.DownloadURL, opts: fetchOpts{
		wantSHA256:  doc.Shasum,
		contentType: "application/zip",
		verify:      func(spool io.ReadSeeker) error { return verifyTFArchive("zip", spool) },
	}})
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
		return fmt.Errorf("%w: unusable size", errTFBadSignature)
	case data[0]&0x80 != 0, strings.HasPrefix(string(data), "-----BEGIN PGP"):
		return nil
	}

	return fmt.Errorf("%w: not a PGP signature", errTFBadSignature)
}

// tfProviderSums returns the digests the upstream's SHA256SUMS lists, fetching
// the file (cached for the repository TTL) when needed.
func (s *Service) tfProviderSums(
	ctx context.Context,
	repo *models.Repository,
	typ, version string,
	doc *tfPackageDoc,
) (map[string]string, error) {
	d := tfFileDoc(repo, tfProviderFileKey(typ, version, urlBase(doc.ShasumsURL)), doc.ShasumsURL, "terraform checksum file", tfSumsOpts)

	sums, _, err := parseDoc(ctx, s, repo, d, func(body []byte, _ *resolution) (map[string]string, error) {
		return parseSHA256SUMS(body)
	})

	return sums, err
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
		s.writeTFError(w, err)
		return
	}

	target, o := doc.ShasumsURL, tfSumsOpts
	if d.Kind == terraform.DownloadProviderSumsSig {
		target, o = doc.ShasumsSignatureURL, fetchOpts{contentType: "application/pgp-signature", verify: verifyTFSignature}
	}

	if urlBase(target) != d.File {
		writeTFErrors(w, http.StatusNotFound, "not found")
		return
	}

	// The checksum file and its signature are minted next to the platform's
	// zip, which is what the mint checked: they follow its quarantine, which
	// the serve of the zip itself checks by cache key and entry id.
	if s.tfZipQuarantined(w, r, repo, tfZipKey(d.A, d.B, d.C, doc.Filename)) {
		return
	}

	key := tfProviderFileKey(d.A, d.B, d.File)

	s.serveTFCached(w, r, repo, key, s.fetchDoc(repo, tfFileDoc(repo, key, target, "terraform provider file", o)))
}

// tfZipQuarantined refuses the request (and returns true) when the cached zip
// stored under zipKey is quarantined by its key or by its cache entry's row id.
func (s *Service) tfZipQuarantined(w http.ResponseWriter, r *http.Request, repo *models.Repository, zipKey string) bool {
	keys := quarantine.PathKeys(zipKey)

	entry, err := s.db.GetProxyCacheEntry(r.Context(), repo.ID, zipKey)
	if err != nil {
		s.log.Errorw("failed to read proxy cache entry", "repo", repo.Name, "path", zipKey, "error", err)
		writeTFErrors(w, http.StatusInternalServerError, "internal error")

		return true
	}

	if entry != nil {
		keys = quarantine.PathKeys(zipKey, entry.ID)
	}

	return quarantine.Enforce(w, r, s.db, repo.ID, keys...)
}

// TerraformHooks is the proxy hooks of the terraform format.
var TerraformHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).serveTerraform,
	// Provider zips, checked against the SHA256SUMS and package document
	// checksums; module archives have no checksum upstream (see terraform.go).
	Immutable:   tfProviderZipKey,
	IsMetadata:  func(key string) bool { return !tfArchiveKey(key) },
	PackageExts: tfArchiveExts,
	BlobPath:    presign.TerraformBlobPath,
}
