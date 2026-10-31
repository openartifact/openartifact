// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/presign"
	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

// NuGet proxy repositories mirror a v3 feed whose service index is the
// repository's upstream_url (default https://api.nuget.org/v3/index.json). The
// repository serves the same /v3/ routes as a hosted NuGet repository, so a
// client uses HOST/repos/REPO/v3/index.json as its source:
//
//   - The service index is fetched from upstream_url, cached as received under
//     "v3/index.json" and served rewritten: the flat container, registrations
//     and search endpoints it advertises point at this repository. Publishing
//     and the other resources (catalog, signatures, ...) are not advertised.
//   - Flat container version lists, registration indexes, pages and leaves are
//     fetched from the upstream resources the index names, cached as received
//     (the registration hosts answer gzip even to Accept-Encoding: identity;
//     the cache keeps whatever bytes arrived) and served with every URL under
//     the upstream's registration and package base rewritten to this
//     repository. Registration pages the upstream does not inline are served
//     at /v3/registration/<id>/page/<lower>/<upper>.json.
//   - .nupkg and .nuspec files are cached under "v3/package/<id>/<version>/<file>",
//     immutable. A .nupkg is checked against the packageHash/packageSize of its
//     catalog entry (reached through the registration leaf's catalogEntry) and,
//     whatever the upstream publishes, must be a zip with a nuspec; a body
//     that fails is refused and never cached.
//   - Search and autocomplete are passed through uncached.
//   - With a minimum age the versions published too recently are left out of
//     the registration and flat container documents and their files refused
//     (nuget_minage.go).
//
// Ids and versions are lower-cased (versions normalized) as the protocol
// prescribes, so every spelling shares one cache entry.

const (
	// maxNuGetDocBytes bounds a metadata document read into memory,
	// decompressed.
	maxNuGetDocBytes = 128 << 20

	// nugetMetaMaxAge is how long shared caches may reuse a public
	// repository's metadata before revalidating.
	nugetMetaMaxAge = 60

	nugetIndexKey = "v3/index.json"

	// nugetJSON is the content type of the NuGet documents served.
	nugetJSON = "application/json"

	// maxNuGetIndexCache bounds the parsed service indexes kept in memory.
	maxNuGetIndexCache = 64
)

var errNotNuGetDoc = errors.New("upstream answer is not a NuGet document")

type nugetKind int

const (
	nugetIndex nugetKind = iota + 1
	nugetVersions
	nugetPackageFile
	nugetRegIndex
	nugetRegPage
	nugetRegLeaf
	nugetQuery
	nugetAutocomplete
)

// nugetPath is a parsed repository-relative request path.
type nugetPath struct {
	kind nugetKind
	// id is the lower-cased package id; version the normalized version.
	id, version string
	// lower and upper bound a registration page.
	lower, upper string
	// file is the lower-cased file name of a package file.
	file string
}

// key is the cache key of the document: the path it is served at, with id and
// version in canonical form.
func (p nugetPath) key() string {
	switch p.kind {
	case nugetVersions:
		return "v3/package/" + p.id + "/index.json"
	case nugetPackageFile:
		return "v3/package/" + p.id + "/" + p.version + "/" + p.file
	case nugetRegIndex:
		return "v3/registration/" + p.id + "/index.json"
	case nugetRegPage:
		return "v3/registration/" + p.id + "/page/" + p.lower + "/" + p.upper + ".json"
	case nugetRegLeaf:
		return "v3/registration/" + p.id + "/" + p.version + ".json"
	default:
		return nugetIndexKey
	}
}

// safeNuGetSegment reports whether s may be one path segment of an id or
// version: the characters NuGet allows in them, and never "." or "..".
func safeNuGetSegment(s string) bool {
	if len(s) > 256 || !pathseg.Name(s) {
		return false
	}

	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '.' && c != '-' && c != '_' && c != '+' {
			return false
		}
	}

	return true
}

// parseNuGetPath classifies relPath. Ids are matched case-insensitively and
// versions in any spelling (see pkgnuget.Normalize).
func parseNuGetPath(relPath string) (nugetPath, bool) {
	segs := strings.Split(strings.Trim(relPath, "/"), "/")
	if len(segs) < 2 || segs[0] != "v3" {
		return nugetPath{}, false
	}

	switch {
	case len(segs) == 2 && segs[1] == "index.json":
		return nugetPath{kind: nugetIndex}, true
	case len(segs) == 2 && segs[1] == "query":
		return nugetPath{kind: nugetQuery}, true
	case len(segs) == 2 && segs[1] == "autocomplete":
		return nugetPath{kind: nugetAutocomplete}, true
	case segs[1] == "package":
		return parseNuGetPackagePath(segs[2:])
	case segs[1] == "registration":
		return parseNuGetRegistrationPath(segs[2:])
	}

	return nugetPath{}, false
}

// parseNuGetPackagePath parses the flat container paths below /v3/package/.
func parseNuGetPackagePath(segs []string) (nugetPath, bool) {
	if len(segs) < 2 || !safeNuGetSegment(segs[0]) {
		return nugetPath{}, false
	}

	id := strings.ToLower(segs[0])

	if len(segs) == 2 && segs[1] == "index.json" {
		return nugetPath{kind: nugetVersions, id: id}, true
	}

	if len(segs) != 3 || !safeNuGetSegment(segs[1]) {
		return nugetPath{}, false
	}

	version, _ := pkgnuget.Normalize(segs[1])
	file := strings.ToLower(segs[2])

	switch file {
	case id + "." + version + ".nupkg", id + "." + strings.ToLower(segs[1]) + ".nupkg":
		file = id + "." + version + ".nupkg"
	case id + ".nuspec":
	default:
		return nugetPath{}, false
	}

	return nugetPath{kind: nugetPackageFile, id: id, version: version, file: file}, true
}

// parseNuGetRegistrationPath parses the paths below /v3/registration/.
func parseNuGetRegistrationPath(segs []string) (nugetPath, bool) {
	if len(segs) < 2 || !safeNuGetSegment(segs[0]) {
		return nugetPath{}, false
	}

	id := strings.ToLower(segs[0])

	switch {
	case len(segs) == 2 && segs[1] == "index.json":
		return nugetPath{kind: nugetRegIndex, id: id}, true
	case len(segs) == 2:
		raw, ok := strings.CutSuffix(segs[1], ".json")
		if !ok || !safeNuGetSegment(raw) {
			return nugetPath{}, false
		}

		version, _ := pkgnuget.Normalize(raw)

		// "Index.json" is not a version: its canonical key, <id>/index.json, is
		// the registration index's.
		if version == "index" {
			return nugetPath{}, false
		}

		return nugetPath{kind: nugetRegLeaf, id: id, version: version}, true
	case len(segs) == 4 && segs[1] == "page":
		upper, ok := strings.CutSuffix(segs[3], ".json")
		if !ok || !safeNuGetSegment(segs[2]) || !safeNuGetSegment(upper) {
			return nugetPath{}, false
		}

		return nugetPath{kind: nugetRegPage, id: id, lower: segs[2], upper: upper}, true
	}

	return nugetPath{}, false
}

func (s *Service) serveNuGet(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, ok := parseNuGetPath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch p.kind {
	case nugetIndex:
		s.serveNuGetIndex(w, r, repo)
	case nugetPackageFile:
		s.serveNuGetPackageFile(w, r, repo, p)
	case nugetQuery, nugetAutocomplete:
		s.serveNuGetSearch(w, r, repo, p.kind)
	default:
		s.serveNuGetDoc(w, r, repo, p)
	}
}

// nugetIndexCache keeps parsed service indexes by the blob they came from
// (content-addressed, so an entry never goes stale).
type nugetIndexCache struct {
	mu sync.Mutex
	m  map[string]*pkgnuget.ServiceIndex
}

func (c *nugetIndexCache) get(blobKey string) (*pkgnuget.ServiceIndex, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	idx, ok := c.m[blobKey]

	return idx, ok
}

func (c *nugetIndexCache) put(blobKey string, idx *pkgnuget.ServiceIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.m == nil || len(c.m) >= maxNuGetIndexCache {
		c.m = make(map[string]*pkgnuget.ServiceIndex)
	}

	c.m[blobKey] = idx
}

// nugetIndexDoc is the upstream service index, fetched from the repository's
// upstream_url itself.
func nugetIndexDoc(repo *models.Repository) metaDoc {
	return metaDoc{
		key:   nugetIndexKey,
		url:   func(string) (string, error) { return repo.UpstreamURL, nil },
		opts:  staticOpts(fetchOpts{accept: "application/json", verify: verifyNuGetObject}),
		limit: maxNuGetDocBytes,
		what:  "nuget service index",
		bad:   badDocUpstream,
	}
}

// resolveNuGetIndex returns the cache entry and the parsed form of the
// upstream service index, fetching it when absent or expired (and serving it
// stale when the upstream is down). An index the upstream does not have or
// that is unusable is an upstream error, not a missing file.
func (s *Service) resolveNuGetIndex(ctx context.Context, repo *models.Repository) (*resolution, *pkgnuget.ServiceIndex, error) {
	d := nugetIndexDoc(repo)

	res, err := s.resolveDoc(ctx, repo, d)
	if errors.Is(err, errNotFound) {
		err = errUpstream
	}

	if err != nil {
		return nil, nil, err
	}

	if idx, ok := s.nuget.get(res.entry.BlobKey); ok {
		return res, idx, nil
	}

	res, body, release, err := s.loadDoc(ctx, repo, d, res)
	if err != nil {
		return nil, nil, err
	}

	idx, perr := parseNuGetIndex(body)

	release()

	if perr != nil {
		return nil, nil, s.unusableDoc(ctx, repo, d, perr)
	}

	s.nuget.put(res.entry.BlobKey, idx)

	return res, idx, nil
}

// parseNuGetIndex parses a cached service index body, which may be gzipped.
func parseNuGetIndex(body []byte) (*pkgnuget.ServiceIndex, error) {
	doc, err := gunzipDoc(body)
	if err != nil {
		return nil, err
	}

	return pkgnuget.ParseServiceIndex(doc) //nolint:wrapcheck // logged by unusableDoc
}

// gunzipDoc returns b decompressed when it is gzip data, b itself otherwise.
// A document over the limit is the upstream's doing (errUpstream).
func gunzipDoc(b []byte) ([]byte, error) {
	if len(b) > maxNuGetDocBytes {
		return nil, errUpstream
	}

	if len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		return b, nil
	}

	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("open gzip document: %w", err)
	}

	out, err := io.ReadAll(io.LimitReader(zr, maxNuGetDocBytes+1))
	if err != nil {
		return nil, fmt.Errorf("decompress document: %w", err)
	}

	if len(out) > maxNuGetDocBytes {
		return nil, errUpstream
	}

	return out, nil
}

// verifyNuGetObject refuses an upstream metadata document that is not a JSON
// object (an HTML error page served with 200, a truncated document).
func verifyNuGetObject(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	body, err := io.ReadAll(io.LimitReader(spool, maxNuGetDocBytes+1))
	if err != nil {
		return fmt.Errorf("read spool: %w", err)
	}

	doc, err := gunzipDoc(body)
	if err != nil {
		return errNotNuGetDoc
	}

	if err := pkgnuget.IsObject(doc); err != nil {
		return errNotNuGetDoc
	}

	return nil
}

// verifyNuspec refuses a .nuspec that is not an XML <package> document.
func verifyNuspec(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	dec := xml.NewDecoder(bufio.NewReader(io.LimitReader(spool, 4<<20)))
	// Only the root element is looked at; whatever the declared encoding, the
	// ASCII markup up to it reads the same.
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }

	for {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: %w", pkgnuget.ErrBadPackage, err)
		}

		if se, ok := tok.(xml.StartElement); ok {
			if se.Name.Local != "package" {
				return fmt.Errorf("%w: root element %q", pkgnuget.ErrBadPackage, se.Name.Local)
			}

			return nil
		}
	}
}

// nugetRewrites are the URL prefixes of the upstream resources and where this
// repository serves them.
func nugetRewrites(idx *pkgnuget.ServiceIndex, repoURL string) []pkgnuget.URLPrefix {
	return []pkgnuget.URLPrefix{
		{From: idx.Registrations, To: repoURL + "/v3/registration/"},
		{From: idx.PackageBase, To: repoURL + "/v3/package/"},
	}
}

// nugetRepoURL is the repository's root URL as the client reached it (BASE_URL
// when set, else the request's origin). NuGet clients take the service index
// @ids as full URLs, and the rewritten documents are built from it per request:
// the cached upstream copies never hold it, and every validator folds it in.
func (s *Service) nugetRepoURL(w http.ResponseWriter, r *http.Request, repo *models.Repository) string {
	return s.publicBase(w, r) + "/repos/" + repo.Name
}

func nugetETag(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))

	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *Service) serveNuGetIndex(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	res, idx, err := s.resolveNuGetIndex(r.Context(), repo)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	body, err := idx.Render(s.nugetRepoURL(w, r, repo))
	if err != nil {
		s.log.Errorw("failed to render nuget document", "repo", repo.Name, "path", nugetIndexKey, "error", err)
		s.failWithoutValidators(w, r, errInternal)

		return
	}

	s.serveGeneratedJSON(w, r, repo, res, body, nugetMetaMaxAge)
}

// nugetDocURL is where the upstream serves the metadata document p names, ""
// when its service index has no resource for it.
func nugetDocURL(idx *pkgnuget.ServiceIndex, p nugetPath) string {
	switch p.kind {
	case nugetVersions:
		if idx.PackageBase != "" {
			return idx.PackageBase + p.id + "/index.json"
		}
	case nugetRegIndex:
		if idx.Registrations != "" {
			return idx.Registrations + p.id + "/index.json"
		}
	case nugetRegLeaf:
		if idx.Registrations != "" {
			return idx.Registrations + p.id + "/" + p.version + ".json"
		}
	}

	return ""
}

// nugetDoc is the metadata document p names, fetched from the upstream resource
// idx names; the cached copy is whatever bytes arrived (possibly gzipped).
func (s *Service) nugetDoc(repo *models.Repository, idx *pkgnuget.ServiceIndex, p nugetPath) metaDoc {
	return metaDoc{
		key: p.key(),
		locate: func(fctx context.Context) (string, error) {
			target := nugetDocURL(idx, p)

			if p.kind == nugetRegPage {
				var err error

				if target, err = s.nugetPageURL(fctx, repo, idx, p); err != nil {
					return "", err
				}
			}

			if target == "" {
				return "", errNotFound
			}

			return target, nil
		},
		opts: func(_ context.Context, target string) fetchOpts {
			return fetchOpts{
				accept: "application/json",
				// Credentials go only to the configured upstream, never to a
				// host the service index points at.
				noAuth: !sameOriginStr(repo.UpstreamURL, target),
				verify: verifyNuGetObject,
			}
		},
		limit: maxNuGetDocBytes,
		what:  "nuget document",
		bad:   badDocUpstream,
	}
}

// nugetPageURL finds, in the cached registration index of p's package, the
// upstream URL of the page spanning p.lower..p.upper. A page the index does
// not list is errNotFound without a negative cache entry: the index may
// simply be stale.
func (s *Service) nugetPageURL(ctx context.Context, repo *models.Repository, idx *pkgnuget.ServiceIndex, p nugetPath) (string, error) {
	pages, _, err := parseDoc(ctx, s, repo, s.nugetDoc(repo, idx, nugetPath{kind: nugetRegIndex, id: p.id}),
		func(body []byte, _ *resolution) ([]pkgnuget.Page, error) {
			doc, gerr := gunzipDoc(body)
			if gerr != nil {
				return nil, gerr
			}

			return pkgnuget.ParsePages(doc) //nolint:wrapcheck // logged by parseDoc
		})
	if err != nil {
		return "", err
	}

	target, ok := pkgnuget.PageURL(pages, p.lower, p.upper)
	if !ok {
		return "", errNotFound
	}

	return target, nil
}

// serveNuGetDoc answers a flat container version list or a registration
// index, page or leaf from the cached upstream document.
func (s *Service) serveNuGetDoc(w http.ResponseWriter, r *http.Request, repo *models.Repository, p nugetPath) {
	idxRes, idx, err := s.resolveNuGetIndex(r.Context(), repo)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	if idxRes.stale {
		w.Header().Set("Warning", staleWarning)
	}

	repoURL := s.nugetRepoURL(w, r, repo)

	gate, err := s.nugetGateFor(r.Context(), repo, idx, p, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: s.nugetDoc(repo, idx, p),
		maxAge:  nugetMetaMaxAge,
		// The rendering depends on the service index and the repository URL
		// (origin included) as well as on the cached document, and on the
		// minimum age gate (which hides versions until they are old enough).
		etag: func(blobKey string) string {
			if stamp := gate.Stamp(); stamp != "" {
				return nugetETag(blobKey, idxRes.entry.BlobKey, repoURL, stamp)
			}

			return nugetETag(blobKey, idxRes.entry.BlobKey, repoURL)
		},
		render: func(body []byte, _ *resolution) ([]byte, string, error) {
			doc, gerr := gunzipDoc(body)
			if gerr != nil {
				return nil, "", gerr
			}

			if doc, gerr = gate.apply(doc, p); gerr != nil {
				return nil, "", gerr
			}

			out, rerr := renderNuGetDoc(doc, p, idx, repoURL)

			return out, nugetJSON, rerr
		},
	})
}

// renderNuGetDoc points the URLs of an upstream metadata document at this
// repository.
func renderNuGetDoc(doc []byte, p nugetPath, idx *pkgnuget.ServiceIndex, repoURL string) ([]byte, error) {
	switch p.kind {
	case nugetRegIndex:
		return pkgnuget.RewriteRegistrationIndex(
			doc,
			repoURL+"/v3/registration/"+p.id+"/page/",
			nugetRewrites(idx, repoURL)...) //nolint:wrapcheck // logged by caller
	case nugetRegPage, nugetRegLeaf:
		return pkgnuget.RewriteURLs(doc, nugetRewrites(idx, repoURL)...) //nolint:wrapcheck // logged by caller
	default:
		// Version lists carry no URLs.
		return doc, nil
	}
}

// serveNuGetPackageFile answers a .nupkg or .nuspec. Curation is checked here,
// before the cache is read, so a file cached before the rules changed is refused
// too; the minimum age needs the publish time, which only a fetch looks up (see
// locateNuGetPackageFile), so a cached file of a version that has aged in is
// served as it is.
func (s *Service) serveNuGetPackageFile(w http.ResponseWriter, r *http.Request, repo *models.Repository, p nugetPath) {
	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	if err := checkGate(g, p.id, p.version, time.Time{}); err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveDownload(w, r, repo, p.key(), func(ctx context.Context) (download, error) {
		return s.locateNuGetPackageFile(ctx, repo, g, p)
	})
}

// locateNuGetPackageFile finds the .nupkg or .nuspec p names in the flat
// container the service index advertises; g refuses a version it holds back.
func (s *Service) locateNuGetPackageFile(ctx context.Context, repo *models.Repository, g versionGate, p nugetPath) (download, error) {
	_, idx, err := s.resolveNuGetIndex(ctx, repo)
	if err != nil {
		return download{}, err
	}

	if idx.PackageBase == "" {
		return download{}, errNotFound
	}

	d := download{
		url:  idx.PackageBase + p.id + "/" + p.version + "/" + p.file,
		opts: fetchOpts{verify: verifyNuspec},
	}

	nupkg := strings.HasSuffix(p.file, ".nupkg")

	if nupkg || g.AgeActive() {
		meta, merr := s.nugetLeafMeta(ctx, repo, idx, p, nupkg, g.AgeActive())
		if merr != nil {
			return download{}, merr
		}

		if gerr := checkGate(g, p.id, p.version, meta.published); gerr != nil {
			return download{}, gerr
		}

		if nupkg {
			d.opts.verify = func(spool io.ReadSeeker) error { return verifyNupkg(spool, meta.digest) }
			// Without a published hash (leaf or catalog entry missing) only the
			// structure is checked. A size alone does not vouch for the content.
			d.opts.unverified = len(meta.digest.Hash) == 0
		}
	}

	return d, nil
}

// verifyNupkg refuses a package that contradicts its published digest or is
// not a zip with a nuspec.
func verifyNupkg(spool io.ReadSeeker, digest pkgnuget.Digest) error {
	if digest.Known() {
		if err := digest.Verify(spool); err != nil {
			return err //nolint:wrapcheck // logged by store
		}
	}

	size, err := spool.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("measure spool: %w", err)
	}

	at, ok := spool.(io.ReaderAt)
	if !ok {
		return fmt.Errorf("%w: spool is not seekable", errInternal)
	}

	return pkgnuget.ValidatePackage(at, size) //nolint:wrapcheck // logged by store
}

// fetchNuGetDocument GETs a small JSON document without caching it (catalog
// entries, search results) and returns it decompressed.
func (s *Service) fetchNuGetDocument(ctx context.Context, repo *models.Repository, target string) ([]byte, error) {
	if verr := netutil.ValidateOutboundURLContext(ctx, target); verr != nil {
		s.log.Warnw("proxy upstream rejected by outbound URL policy", "repo", repo.Name, "error", verr)
		return nil, outboundError(verr)
	}

	req, err := s.newUpstreamRequest(ctx, repo, target, fetchOpts{
		accept: "application/json",
		noAuth: !sameOriginStr(repo.UpstreamURL, target),
	})
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warnw("proxy upstream request failed", "repo", repo.Name, "error", err)
		return nil, errUpstreamDown
	}
	defer func() { base.CloseLog(resp.Body, s.log) }()

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, errNotFound
	case resp.StatusCode >= http.StatusInternalServerError:
		s.log.Warnw("proxy upstream returned an error", "repo", repo.Name, "status", resp.StatusCode)
		return nil, errUpstreamDown
	case resp.StatusCode != http.StatusOK:
		s.log.Warnw("proxy upstream returned an error", "repo", repo.Name, "status", resp.StatusCode)
		return nil, errUpstream
	}

	if aerr := s.packuments.Acquire(ctx); aerr != nil {
		return nil, aerr //nolint:wrapcheck // caller checks for cancellation
	}
	defer s.packuments.Release()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxNuGetDocBytes+1))
	if err != nil || (resp.ContentLength >= 0 && int64(len(body)) != resp.ContentLength) {
		s.log.Warnw("proxy upstream body read failed", "repo", repo.Name, "error", err)
		return nil, errUpstreamDown
	}

	doc, err := gunzipDoc(body)
	if err != nil {
		s.log.Warnw("proxy upstream document unreadable", "repo", repo.Name, "error", err)
		return nil, errUpstream
	}

	return doc, nil
}

// serveNuGetSearch passes a search or autocomplete query through to the
// upstream endpoint the service index advertises. Results are not cached; the
// registration and package URLs in them point at this repository.
func (s *Service) serveNuGetSearch(w http.ResponseWriter, r *http.Request, repo *models.Repository, kind nugetKind) {
	_, idx, err := s.resolveNuGetIndex(r.Context(), repo)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	endpoint := idx.Search
	if kind == nugetAutocomplete {
		endpoint = idx.Autocomplete
	}

	u, perr := url.Parse(endpoint)
	if endpoint == "" || perr != nil {
		http.NotFound(w, r)
		return
	}

	u.RawQuery, u.Fragment = r.URL.RawQuery, ""

	doc, err := s.fetchNuGetDocument(r.Context(), repo, u.String())
	if err == nil {
		doc, err = pkgnuget.RewriteURLs(doc, nugetRewrites(idx, s.nugetRepoURL(w, r, repo))...)
		if err != nil {
			s.log.Warnw("unreadable upstream nuget search result", "repo", repo.Name, "error", err)
			err = errUpstream
		}
	}

	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	w.Header().Set("Cache-Control", "private, no-cache")
	writeBody(w, r, nugetJSON, doc)
}

// nugetPackageDir is the key prefix of NuGet's flat container; everything
// outside it is metadata.
const nugetPackageDir = "v3/package/"

// nugetImmutable reports whether key is a package file of the flat container:
// not version lists or registrations.
func nugetImmutable(key string) bool {
	return strings.HasPrefix(key, nugetPackageDir) && (strings.HasSuffix(key, ".nupkg") || strings.HasSuffix(key, ".nuspec"))
}

// NuGetHooks is the proxy hooks of the nuget format.
var NuGetHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve:      (*Service).serveNuGet,
	Immutable:  nugetImmutable,
	IsMetadata: func(key string) bool { return !strings.HasPrefix(key, nugetPackageDir) },
	Published:  (*Service).nugetPublished,
	Versions:   (*Service).nugetVersionList,
	BlobPath:   presign.NuGetBlobPath,
}
