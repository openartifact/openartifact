// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/presign"
	pkgconan "github.com/openartifact/openartifact/pkg/conan"
)

// Conan proxy repositories mirror a Conan 2 remote (default
// https://center2.conan.io); clients add HOST/repos/REPO as a remote and speak
// the v2 REST API (see conan_v1.go for Conan 1 clients). Ping and the login
// endpoints are answered by the dispatcher: they authenticate the client to
// OpenArtifact, not to the upstream.
//
//   - Revision lists and the latest endpoints (recipe and package) are cached
//     under their path for the repository's cache TTL and served stale while the
//     upstream is down. Their bodies must be what the API sends or they are
//     refused and never cached.
//   - File lists and files below a revision are cached immutably: a revision
//     is addressed by the digest of its content, so its path never names other
//     bytes. A file list is only pinned once it lists conanmanifest.txt, which
//     the client uploads last, so a half-uploaded revision is revalidated.
//   - Searches (recipes, and the packages of a recipe revision) are passed
//     through uncached: their results are open-ended in the query.
//
// With upstream credentials the proxy logs in like the conan client does:
// the username and password go to {upstream}/v2/users/authenticate and the
// token it returns is sent as a Bearer token (cached for a few minutes, renewed
// when the upstream answers 401); an upstream without that endpoint gets Basic
// credentials. Without a username the password is itself the token, sent as a
// Bearer token as is.

// maxConanDocBytes bounds a search answer held in memory (a search of a large
// remote lists every recipe).
const maxConanDocBytes = 64 << 20

// maxConanQueryLen bounds one search parameter handed to the upstream.
const maxConanQueryLen = 512

// maxConanLoginBody bounds the answer of the login endpoint (a token).
const maxConanLoginBody = 64 << 10

// conanSearchParams are the query parameters of the search endpoints that are
// forwarded to the upstream.
var conanSearchParams = []string{"q", "ignorecase", "list_only"} //nolint:gochecknoglobals // read-only table

func (s *Service) serveConan(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	if p, ok := pkgconan.ParsePath(relPath); ok {
		s.serveConanV2(w, r, repo, p)
		return
	}

	if p, ok := pkgconan.ParseV1Path(relPath); ok {
		s.serveConanV1(w, r, repo, p)
		return
	}

	http.NotFound(w, r)
}

func (s *Service) serveConanV2(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgconan.Path) {
	switch p.Kind {
	case pkgconan.KindSearch:
		s.serveConanPassthrough(w, r, repo, p.String(), checkSearch)
	case pkgconan.KindPackageSearch:
		s.serveConanPassthrough(w, r, repo, p.String(), checkPackageSearch)
	case pkgconan.KindRecipeLatest, pkgconan.KindRecipeRevisions, pkgconan.KindPackageLatest, pkgconan.KindPackageRevisions,
		pkgconan.KindFiles, pkgconan.KindFile:
		s.serveConanCached(w, r, repo, p)
	case pkgconan.KindOther:
		http.NotFound(w, r)
	}
}

func (s *Service) serveConanCached(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgconan.Path) {
	s.serveCachedFetch(w, r, repo, p.String(), func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchConan(fctx, repo, p)
	})
}

// conanFetchOpts is how one cached path is fetched and checked. It is built
// per attempt: the file list's vouched flag is the attempt's own.
func conanFetchOpts(p pkgconan.Path) fetchOpts {
	o := fetchOpts{accept: "application/json", contentType: "application/json"}

	switch p.Kind {
	case pkgconan.KindRecipeLatest, pkgconan.KindPackageLatest:
		o.verify = verifyConanDoc(func(doc []byte) error {
			_, err := pkgconan.ParseRevision(doc)
			return err //nolint:wrapcheck // logged by store
		})

		return o
	case pkgconan.KindRecipeRevisions, pkgconan.KindPackageRevisions:
		o.verify = verifyConanDoc(func(doc []byte) error {
			_, err := pkgconan.ParseRevisions(doc)
			return err //nolint:wrapcheck // logged by store
		})

		return o
	case pkgconan.KindFiles:
		// Pinned only when the list holds the manifest: see the comment above.
		o.vouched = new(bool)
		o.verify = verifyConanDoc(func(doc []byte) error {
			_, published, err := pkgconan.ParseFiles(doc)
			*o.vouched = published

			return err //nolint:wrapcheck // logged by store
		})

		return o
	case pkgconan.KindFile:
		if isGzipName(p.File) {
			return fetchOpts{verify: verifyGzipMagic}
		}
	case pkgconan.KindSearch, pkgconan.KindPackageSearch, pkgconan.KindOther:
	}

	return fetchOpts{}
}

// verifyConanDoc reads the spooled body and refuses it when check does.
func verifyConanDoc(check func(doc []byte) error) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		doc, err := io.ReadAll(io.LimitReader(spool, maxConanDocBytes+1))
		if err != nil {
			return fmt.Errorf("read conan document: %w", err)
		}

		return check(doc)
	}
}

// isGzipName reports whether name is a file the conan client packs with gzip.
func isGzipName(name string) bool {
	return strings.HasSuffix(name, ".tgz") || strings.HasSuffix(name, ".tar.gz")
}

var errNotGzip = errors.New("conan archive is not gzip data")

// verifyGzipMagic refuses a body that is not a gzip stream: the error pages a
// proxy or WAF answers with 200 would otherwise be cached as the package.
func verifyGzipMagic(spool io.ReadSeeker) error {
	var magic [2]byte
	if _, err := io.ReadFull(spool, magic[:]); err != nil || magic != [2]byte{0x1f, 0x8b} {
		return errNotGzip
	}

	return nil
}

// fetchConan downloads one path from the upstream and caches it. The document
// is built per attempt: the login token is part of its fetch options.
func (s *Service) fetchConan(ctx context.Context, repo *models.Repository, p pkgconan.Path) (*models.ProxyCacheEntry, error) {
	var entry *models.ProxyCacheEntry

	err := s.withConanAuth(ctx, repo, func(authz string) error {
		d := metaDoc{
			key: p.String(),
			opts: func(context.Context, string) fetchOpts {
				o := conanFetchOpts(p)
				o.authorization = authz

				return o
			},
		}

		var ferr error

		entry, ferr = s.fetchDoc(repo, d)(ctx)

		return ferr
	})

	return entry, err
}

// serveConanPassthrough passes a search or snapshot through to the upstream
// path. The answer is not cached; it is verified by check to be what the API
// sends.
func (s *Service) serveConanPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	upstreamPath string,
	check func([]byte) error,
) {
	query := make(url.Values, len(conanSearchParams))

	for _, name := range conanSearchParams {
		v := r.URL.Query().Get(name)
		if len(v) > maxConanQueryLen {
			http.Error(w, "search parameter too long", http.StatusBadRequest)
			return
		}

		if v != "" {
			query.Set(name, v)
		}
	}

	target, err := upstreamURL(repo.UpstreamURL, upstreamPath)
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		s.writeResolveError(w, r, errInternal)

		return
	}

	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	doc, err := s.fetchConanDocument(r.Context(), repo, target)
	if err == nil && check(doc) != nil {
		s.log.Warnw("unreadable upstream conan document", "repo", repo.Name, "path", upstreamPath)
		err = errVerify
	}

	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	writeConanJSON(w, r, doc)
}

// writeConanJSON answers a document that is not cached by shared caches.
func writeConanJSON(w http.ResponseWriter, r *http.Request, doc []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Content-Length", strconv.Itoa(len(doc)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(doc)
	}
}

func checkSearch(doc []byte) error {
	_, err := pkgconan.ParseSearch(doc)
	return err //nolint:wrapcheck // logged by the caller
}

func checkPackageSearch(doc []byte) error {
	_, err := pkgconan.ParsePackageSearch(doc)
	return err //nolint:wrapcheck // logged by the caller
}

func checkFileMap(doc []byte) error {
	_, err := pkgconan.ParseFileMap(doc)
	return err //nolint:wrapcheck // logged by the caller
}

// fetchConanDocument GETs a JSON document from the upstream without caching it.
func (s *Service) fetchConanDocument(ctx context.Context, repo *models.Repository, target string) ([]byte, error) {
	var doc []byte

	err := s.withConanAuth(ctx, repo, func(authz string) error {
		var derr error

		doc, derr = s.getConanDocument(ctx, repo, target, authz)

		return derr
	})

	return doc, err
}

func (s *Service) getConanDocument(ctx context.Context, repo *models.Repository, target, authz string) ([]byte, error) {
	if verr := netutil.ValidateOutboundURLContext(ctx, target); verr != nil {
		s.log.Warnw("proxy upstream rejected by outbound URL policy", "repo", repo.Name, "error", verr)
		return nil, outboundError(verr)
	}

	req, err := s.newUpstreamRequest(ctx, repo, target, fetchOpts{accept: "application/json", authorization: authz})
	if err != nil {
		return nil, err
	}

	if aerr := s.packuments.Acquire(ctx); aerr != nil {
		return nil, aerr //nolint:wrapcheck // caller checks for cancellation
	}
	defer s.packuments.Release()

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warnw("proxy upstream request failed", "repo", repo.Name, "error", err)
		return nil, errUpstreamDown
	}
	defer func() { base.CloseLog(resp.Body, s.log) }()

	if serr := s.upstreamStatusError(repo, "", resp.StatusCode); serr != nil {
		return nil, serr
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConanDocBytes+1))
	if err != nil || (resp.ContentLength >= 0 && int64(len(body)) != resp.ContentLength) {
		s.log.Warnw("proxy upstream body read failed", "repo", repo.Name, "error", err)
		return nil, errUpstreamDown
	}

	if len(body) > maxConanDocBytes {
		s.log.Warnw("proxy upstream document exceeds size limit", "repo", repo.Name)
		return nil, errUpstream
	}

	return body, nil
}

// withConanAuth runs op with the Authorization header the upstream expects ("" for
// an anonymous upstream). A cached token the upstream no longer accepts (op
// fails with errUnauthorized) is dropped and op runs once more with a fresh one.
func (s *Service) withConanAuth(ctx context.Context, repo *models.Repository, op func(authz string) error) error {
	authz, cached, err := s.conanAuthorization(ctx, repo)
	if err != nil {
		return err
	}

	err = op(authz)
	if !errors.Is(err, errUnauthorized) || !cached {
		return err
	}

	s.tokens.drop(conanTokenKey(repo))

	authz, _, err = s.conanAuthorization(ctx, repo)
	if err != nil {
		return err
	}

	return op(authz)
}

func conanTokenKey(repo *models.Repository) string {
	return strings.Join([]string{"conan", repo.ID, repo.UpstreamURL, repo.UpstreamUsername, repo.UpstreamPasswordEnc}, "\x00")
}

// conanAuthorization returns the Authorization header value to use against the
// upstream; cached reports that it came from the token cache.
func (s *Service) conanAuthorization(ctx context.Context, repo *models.Repository) (string, bool, error) {
	if repo.UpstreamUsername == "" && repo.UpstreamPasswordEnc == "" {
		return "", false, nil
	}

	password := ""

	if repo.UpstreamPasswordEnc != "" {
		var err error

		password, err = crypto.Decrypt(repo.UpstreamPasswordEnc)
		if err != nil {
			s.log.Errorw("failed to decrypt proxy upstream password", "repo", repo.Name, "error", err)
			return "", false, errInternal
		}
	}

	if repo.UpstreamUsername == "" {
		return "Bearer " + password, false, nil
	}

	key := conanTokenKey(repo)
	if h := s.tokens.get(key); h != "" {
		return h, true, nil
	}

	authz, err := s.conanLogin(ctx, repo, password)
	if err != nil {
		return "", false, err
	}

	s.tokens.put(key, authz, maxTokenTTL)

	return authz, false, nil
}

// conanLogin exchanges the repository's credentials for the Authorization
// header value: the token of the upstream's login endpoint as a Bearer, or
// Basic credentials for an upstream that has no such endpoint.
func (s *Service) conanLogin(ctx context.Context, repo *models.Repository, password string) (string, error) {
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(repo.UpstreamUsername+":"+password))

	target, err := upstreamURL(repo.UpstreamURL, "v2/users/authenticate")
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return "", errInternal
	}

	if verr := netutil.ValidateOutboundURLContext(ctx, target); verr != nil {
		s.log.Warnw("proxy upstream rejected by outbound URL policy", "repo", repo.Name, "error", verr)
		return "", outboundError(verr)
	}

	req, err := s.newUpstreamRequest(ctx, repo, target, fetchOpts{accept: "text/plain", authorization: basic})
	if err != nil {
		return "", err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warnw("proxy upstream login failed", "repo", repo.Name, "error", err)
		return "", errUpstreamDown
	}
	defer func() { base.CloseLog(resp.Body, s.log) }()

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return basic, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		s.log.Warnw("proxy upstream rejected the repository credentials", "repo", repo.Name, "status", resp.StatusCode)
		return "", errUpstream
	case resp.StatusCode >= http.StatusInternalServerError:
		s.log.Warnw("proxy upstream login returned an error", "repo", repo.Name, "status", resp.StatusCode)
		return "", errUpstreamDown
	case resp.StatusCode != http.StatusOK:
		s.log.Warnw("proxy upstream login returned an error", "repo", repo.Name, "status", resp.StatusCode)
		return "", errUpstream
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConanLoginBody+1))
	if err != nil {
		s.log.Warnw("proxy upstream login body read failed", "repo", repo.Name, "error", err)
		return "", errUpstreamDown
	}

	token := strings.TrimSpace(string(body))
	if !headerSafeToken(token) || len(body) > maxConanLoginBody {
		s.log.Warnw("proxy upstream login did not return a usable token", "repo", repo.Name)
		return "", errUpstream
	}

	return "Bearer " + token, nil
}

// headerSafeToken reports whether s can be sent as an HTTP header value: not
// empty, printable ASCII without spaces.
func headerSafeToken(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}

	return true
}

// conanRevisionFile reports whether key is the file list or a file of a
// recipe or package revision.
func conanRevisionFile(key string) bool {
	return strings.Contains(key, "/revisions/") && (strings.HasSuffix(key, "/files") || strings.Contains(key, "/files/"))
}

// ConanHooks is the proxy hooks of the conan format.
var ConanHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).serveConan,
	// File lists and files below a revision, which a digest of its content
	// names; revision lists and latest move.
	Immutable:  conanRevisionFile,
	IsMetadata: func(key string) bool { return !strings.Contains(key, "/files/") },
	BlobPath:   presign.ConanBlobPath,
	// The recipe of a Conan 2 path: gated before Serve (see conanCoordinates).
	Coordinates: conanCoordinates,
}
