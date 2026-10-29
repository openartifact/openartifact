// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/netutil"
	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

// Ansible proxy repositories mirror a Galaxy server (default
// https://galaxy.ansible.com); clients point ansible-galaxy at
// HOST/repos/REPO/api/. Every link a client sees is regenerated for this
// repository (pkgansible.Links): the upstream's hrefs and its download host are
// never handed out.
//
//   - api/ and api/v3/ are generated, like the hosted repository's.
//   - api/v3/collections/ (the index) answers 404: an upstream cannot be
//     enumerated.
//   - The version list of a collection is fetched upstream page by page
//     (limit/offset, bounded by ansibleMaxPages and ansibleMaxVersions),
//     normalized, and cached as one document under
//     "api/v3/collections/<ns>/<name>/versions/" with the repository's cache
//     TTL and serve-stale rules. The collection document is generated from it
//     and clients are served pages of our own.
//   - A version detail document is cached verbatim under
//     "api/v3/collections/<ns>/<name>/versions/<version>/" once it is
//     well formed (pkgansible.ParseVersionDoc) and relinked when served.
//   - A tarball (api/v3/artifacts/collections/<file>) is fetched from the
//     download_url of its version document (which may be on another host, then
//     without the repository's credentials) and cached, immutable, only when its
//     sha256 equals the artifact.sha256 of that document. Other bytes are
//     refused and never cached.
//
// The upstream is asked for api/v3/collections/...; Galaxy answers with a
// redirect to its canonical plugin path, which the client follows.

const (
	// ansibleArtifactDir is the key prefix of the tarballs.
	ansibleArtifactDir = "api/v3/artifacts/collections/"

	// ansiblePageSize is the page size asked of the upstream's version list.
	ansiblePageSize = 100
	// ansibleMaxPages and ansibleMaxVersions bound one collection's version
	// list, whatever the upstream claims to hold.
	ansibleMaxPages    = 100
	ansibleMaxVersions = 5000

	// ansibleMaxAge is how long shared caches may reuse a generated document
	// of a public repository.
	ansibleMaxAge = 60
)

func (s *Service) serveAnsible(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, ok := pkgansible.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case pkgansible.KindRoot:
		s.writeAnsibleDoc(w, r, repo, nil, map[string]any{
			"description":        "Galaxy API of an OpenArtifact Ansible collection repository",
			"available_versions": map[string]string{"v3": "v3/"},
		})
	case pkgansible.KindV3:
		l := s.ansibleLinks(repo)
		s.writeAnsibleDoc(w, r, repo, nil, map[string]string{"collections": l.Collections(), "artifacts": l.Artifacts()})
	case pkgansible.KindCollection:
		s.serveAnsibleCollection(w, r, repo, p)
	case pkgansible.KindVersions:
		s.serveAnsibleVersions(w, r, repo, p)
	case pkgansible.KindVersion:
		s.serveAnsibleVersion(w, r, repo, p)
	case pkgansible.KindDownload:
		s.serveAnsibleDownload(w, r, repo, p)
	default:
		// KindIndex: an upstream cannot be enumerated.
		http.NotFound(w, r)
	}
}

// ansibleLinks builds the links of the repository's own Galaxy API.
func (s *Service) ansibleLinks(repo *models.Repository) pkgansible.Links {
	return pkgansible.Links{Root: strings.TrimSuffix(s.baseURL, "/") + "/repos/" + url.PathEscape(repo.Name) + "/api/v3"}
}

func ansibleVersionsKey(namespace, name string) string {
	return "api/v3/collections/" + namespace + "/" + name + "/versions/"
}

func ansibleVersionKey(namespace, name, version string) string {
	return ansibleVersionsKey(namespace, name) + version + "/"
}

// writeAnsibleDoc answers with a generated JSON document, revalidated by the
// digest of its text. res is the cache resolution it was built from (nil for
// documents that come from no upstream copy): a stale one is flagged.
func (s *Service) writeAnsibleDoc(w http.ResponseWriter, r *http.Request, repo *models.Repository, res *resolution, doc any) {
	body, err := json.Marshal(doc)
	if err != nil {
		s.log.Errorw("failed to encode ansible document", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	s.writeGeneratedJSON(w, r, repo, res, body, ansibleMaxAge)
}

// writeGeneratedJSON answers with an encoded JSON document, revalidated by its
// digest and cacheable by shared caches for maxAge seconds when the repository
// is public. res is the cache resolution it was built from (nil when it came
// from no upstream copy): a stale one is flagged.
func (s *Service) writeGeneratedJSON(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	res *resolution,
	body []byte,
	maxAge int,
) {
	if res != nil && res.stale {
		w.Header().Set("Warning", staleWarning)
	}

	sum := sha256.Sum256(body)
	if base.Revalidate(w, r, repo.Public, `"`+hex.EncodeToString(sum[:16])+`"`, maxAge) {
		return
	}

	w.Header().Set("Content-Type", jsonMediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// readAnsibleDoc loads a cached document under the document gate.
func (s *Service) readAnsibleDoc(ctx context.Context, repo *models.Repository, res *resolution) ([]byte, error) {
	if err := s.packuments.Acquire(ctx); err != nil {
		return nil, err //nolint:wrapcheck // caller checks for cancellation
	}
	defer s.packuments.Release()

	doc, err := s.readCachedDoc(ctx, res.entry, pkgansible.MaxDocBytes)
	if err != nil {
		s.log.Errorw("failed to read cached ansible document", "repo", repo.Name, "key", res.entry.CacheKey, "error", err)
		return nil, errInternal
	}

	return doc, nil
}

// ansibleVersionList returns the cached version list of a collection, newest
// first, fetching it when it is absent or expired.
func (s *Service) ansibleVersionList(
	ctx context.Context, repo *models.Repository, p pkgansible.Path,
) (*resolution, []pkgansible.VersionRef, error) {
	key := ansibleVersionsKey(p.Namespace, p.Name)

	res, err := s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		target, uerr := upstreamURL(repo.UpstreamURL, key)
		if uerr != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", uerr)
			return nil, errInternal
		}

		return s.fetchURL(fctx, repo, key, target, fetchOpts{
			accept:      jsonMediaType,
			contentType: jsonMediaType,
			transform:   func(resp *http.Response) error { return s.ansibleVersionsTransform(fctx, repo, target, resp) },
		})
	})
	if err != nil {
		return nil, nil, err
	}

	doc, err := s.readAnsibleDoc(ctx, repo, res)
	if err != nil {
		return nil, nil, err
	}

	page, err := pkgansible.ParseVersionPage(bytes.NewReader(doc))
	if err != nil {
		s.log.Errorw("cached ansible version list is unusable", "repo", repo.Name, "key", key, "error", err)
		return nil, nil, errInternal
	}

	return res, page.Versions, nil
}

// ansibleVersionsTransform replaces the first page of an upstream version list
// with the whole list, normalized and newest first, in the page format.
func (s *Service) ansibleVersionsTransform(ctx context.Context, repo *models.Repository, target string, resp *http.Response) error {
	first, err := pkgansible.ParseVersionPage(resp.Body)
	if err != nil {
		s.log.Warnw("proxy upstream version list refused", "repo", repo.Name, "error", err)
		return errVerify
	}

	if first.Count > ansibleMaxVersions {
		s.log.Warnw("proxy upstream version list too long", "repo", repo.Name, "count", first.Count)
		return errVerify
	}

	received := len(first.Versions)
	page := first
	seen := make(map[string]struct{}, first.Count)

	for _, v := range first.Versions {
		seen[v.Version] = struct{}{}
	}

	for pages := 1; received < first.Count; pages++ {
		if pages >= ansibleMaxPages {
			s.log.Warnw("proxy upstream version list has too many pages", "repo", repo.Name)
			return errVerify
		}

		next, perr := s.ansiblePage(ctx, repo, target, received)
		if perr != nil {
			return perr
		}

		// A page that adds no version means the upstream ignores the offset
		// (or lied about the count): a truncated list must not be cached.
		added := 0

		for _, v := range next.Versions {
			if _, dup := seen[v.Version]; !dup {
				seen[v.Version] = struct{}{}
				added++
			}
		}

		if added == 0 {
			s.log.Warnw("proxy upstream version list ended early", "repo", repo.Name, "received", received, "count", first.Count)
			return errVerify
		}

		received += len(next.Versions)
		page.Versions = append(page.Versions, next.Versions...)
	}

	body, err := ansibleVersionListBody(page.Versions)

	switch {
	case errors.Is(err, errNotFound):
		return errNotFound
	case err != nil:
		s.log.Warnw("proxy upstream version list refused", "repo", repo.Name, "error", err)
		return errVerify
	}

	replaceBody(resp, body)

	return nil
}

// ansiblePage fetches the page of the version list at target that starts at offset.
func (s *Service) ansiblePage(ctx context.Context, repo *models.Repository, target string, offset int) (*pkgansible.VersionPage, error) {
	pageURL := target + "?limit=" + strconv.Itoa(ansiblePageSize) + "&offset=" + strconv.Itoa(offset)

	if verr := netutil.ValidateOutboundURLContext(ctx, pageURL); verr != nil {
		s.log.Warnw("proxy upstream rejected by outbound URL policy", "repo", repo.Name, "error", verr)
		return nil, outboundError(verr)
	}

	req, err := s.newUpstreamRequest(ctx, repo, pageURL, fetchOpts{accept: jsonMediaType})
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warnw("proxy upstream request failed", "repo", repo.Name, "error", err)
		return nil, errUpstreamDown
	}
	defer func() { base.CloseLog(resp.Body, s.log) }()

	if serr := s.upstreamStatusError(repo, pageURL, resp.StatusCode); serr != nil {
		return nil, serr
	}

	page, err := pkgansible.ParseVersionPage(resp.Body)
	if err != nil {
		s.log.Warnw("proxy upstream version list refused", "repo", repo.Name, "error", err)
		return nil, errVerify
	}

	return page, nil
}

// ansibleVersionListBody is the cached form of a version list: the versions,
// deduplicated and newest first, in the page format. A list without versions is
// a collection that does not exist (errNotFound).
func ansibleVersionListBody(versions []pkgansible.VersionRef) ([]byte, error) {
	seen := make(map[string]struct{}, len(versions))
	refs := make([]pkgansible.VersionRef, 0, len(versions))

	for _, v := range versions {
		if _, dup := seen[v.Version]; dup {
			continue
		}

		seen[v.Version] = struct{}{}

		refs = append(refs, v)
	}

	if len(refs) == 0 {
		return nil, errNotFound
	}

	pkgansible.NewestFirst(refs)

	body, err := json.Marshal(pkgansible.Page{
		Meta:  map[string]int{"count": len(refs)},
		Links: map[string]any{"next": nil},
		Data:  refs,
	})
	if err != nil {
		return nil, fmt.Errorf("encode version list: %w", err)
	}

	return body, nil
}

func (s *Service) serveAnsibleCollection(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgansible.Path) {
	res, refs, err := s.ansibleVersionList(r.Context(), repo, p)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.writeAnsibleDoc(w, r, repo, res, pkgansible.NewCollectionDoc(s.ansibleLinks(repo), p.Namespace, p.Name, refs))
}

func (s *Service) serveAnsibleVersions(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgansible.Path) {
	limit, offset, err := pkgansible.PageParams(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	res, refs, err := s.ansibleVersionList(r.Context(), repo, p)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	l := s.ansibleLinks(repo)
	window := append([]pkgansible.VersionRef{}, pkgansible.Window(refs, limit, offset)...)

	for i := range window {
		window[i].Relink(l, p.Namespace, p.Name)
	}

	page := pkgansible.NewPage(l.Versions(p.Namespace, p.Name), len(refs), limit, offset)
	page.Data = window

	s.writeAnsibleDoc(w, r, repo, res, page)
}

// ansibleVerifyDoc checks a version document as the upstream sends it: it must
// describe the requested version, carry its artifact digest and say where the
// artifact is.
func ansibleVerifyDoc(p pkgansible.Path) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		doc, err := pkgansible.ParseVersionDoc(spool, p.Namespace, p.Name, p.Version)
		if err != nil {
			return fmt.Errorf("version document: %w", err)
		}

		if doc.DownloadURL == "" {
			return fmt.Errorf("%w: version document has no download_url", pkgansible.ErrDoc)
		}

		return nil
	}
}

// ansibleVersionDoc returns the cached detail document of a version, fetching it
// when it is absent or expired.
func (s *Service) ansibleVersionDoc(
	ctx context.Context, repo *models.Repository, p pkgansible.Path,
) (*resolution, *pkgansible.VersionDoc, error) {
	key := ansibleVersionKey(p.Namespace, p.Name, p.Version)

	res, err := s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		target, uerr := upstreamURL(repo.UpstreamURL, key)
		if uerr != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", uerr)
			return nil, errInternal
		}

		return s.fetchURL(fctx, repo, key, target, fetchOpts{
			accept:      jsonMediaType,
			contentType: jsonMediaType,
			verify:      ansibleVerifyDoc(p),
		})
	})
	if err != nil {
		return nil, nil, err
	}

	raw, err := s.readAnsibleDoc(ctx, repo, res)
	if err != nil {
		return nil, nil, err
	}

	doc, err := pkgansible.ParseVersionDoc(bytes.NewReader(raw), p.Namespace, p.Name, p.Version)
	if err != nil {
		s.log.Errorw("cached ansible version document is unusable", "repo", repo.Name, "key", key, "error", err)
		return nil, nil, errInternal
	}

	return res, doc, nil
}

func (s *Service) serveAnsibleVersion(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgansible.Path) {
	res, doc, err := s.ansibleVersionDoc(r.Context(), repo, p)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	doc.Relink(s.ansibleLinks(repo))
	s.writeAnsibleDoc(w, r, repo, res, doc)
}

func (s *Service) serveAnsibleDownload(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgansible.Path) {
	key := ansibleArtifactDir + pkgansible.FileName(p.Namespace, p.Name, p.Version)
	resolve := func() (*resolution, error) {
		return s.resolveWith(r.Context(), repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
			return s.fetchAnsibleTarball(fctx, repo, p, key)
		})
	}

	res, err := resolve()
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

// ansibleDownloadTarget resolves the download_url of a version document against
// the URL the document was fetched from; it must be an absolute http(s) URL.
func ansibleDownloadTarget(docURL, downloadURL string) (string, error) {
	docRef, err := url.Parse(docURL)
	if err != nil {
		return "", fmt.Errorf("parse document url: %w", err)
	}

	ref, err := url.Parse(downloadURL)
	if err != nil {
		return "", fmt.Errorf("parse download url: %w", err)
	}

	target := docRef.ResolveReference(ref)
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return "", fmt.Errorf("%w: unusable download url %q", pkgansible.ErrDoc, downloadURL)
	}

	target.Fragment = ""

	return target.String(), nil
}

// fetchAnsibleTarball downloads a collection tarball from the URL its version
// document names and caches it only when it matches the document's digest. A
// version the upstream does not list is errNotFound.
func (s *Service) fetchAnsibleTarball(
	ctx context.Context, repo *models.Repository, p pkgansible.Path, key string,
) (*models.ProxyCacheEntry, error) {
	_, doc, err := s.ansibleVersionDoc(ctx, repo, p)
	if err != nil {
		return nil, err
	}

	docURL, err := upstreamURL(repo.UpstreamURL, ansibleVersionKey(p.Namespace, p.Name, p.Version))
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return nil, errInternal
	}

	target, err := ansibleDownloadTarget(docURL, doc.DownloadURL)
	if err != nil {
		s.log.Warnw("proxy upstream download url refused", "repo", repo.Name, "path", key, "error", err)
		return nil, errUpstream
	}

	return s.fetchURL(ctx, repo, key, target, fetchOpts{
		// Credentials go only to the configured upstream, never to the host
		// a document points downloads at.
		noAuth:     !sameOriginStr(repo.UpstreamURL, target),
		wantSHA256: doc.Artifact.SHA256,
	})
}
