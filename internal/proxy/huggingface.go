// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // the git blob id of a file, not a security hash: the upstream publishes nothing else for plain files
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/presign"
)

// The Hugging Face Hub proxy.
//
// Clients (huggingface_hub, transformers, the hf CLI) take their endpoint from
// HF_ENDPOINT and build every URL below it. The proxy serves the read side of
// that protocol, pins every revision to a commit, and caches each file once per
// commit (see docs/formats/huggingface.md for the contract):
//
//	GET  api/{models|datasets|spaces}/{id}[/revision/{rev}]        repository info
//	GET  api/{models|datasets|spaces}/{id}/tree/{rev}[/{path}]     file listing
//	HEAD|GET [datasets/|spaces/]{id}/resolve/{rev}/{path}          a file
//
// The cache keys are, with kind and id (lower case) in front of the rest:
//
//	r/…/{rev}        repository info of a ref or a commit (rb/: with ?blobs=true)
//	c/…/{sha}/{p}    page p of the commit history that ends in sha
//	t/…/{sha}/…      one page of a tree listing
//	m/…/{sha}/{path} etag and size of a file (from the upstream's HEAD)
//	f/…/{sha}/{path} the file itself

const (
	hfInfoDir      = "r/"
	hfBlobsDir     = "rb/"
	hfCommitsDir   = "c/"
	hfTreeDir      = "t/"
	hfMetaDir      = "m/"
	hfFileDir      = "f/"
	hfDefaultRev   = "@default"
	hfCommitsPage  = 50
	hfMaxPages     = 10
	hfDocLimit     = 64 << 20
	hfMaxLimit     = 1000
	hfMaxRevLen    = 255
	hfMaxIDPartLen = 96
)

var (
	hfIDPart   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	hfRevOK    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	hfSHA1Hex  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hfSHA256   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hfCursorOK = regexp.MustCompile(`^[A-Za-z0-9_=+/.-]+$`)
	hfNextLink = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)
)

// hfAction is what a request asks for.
type hfAction int

const (
	hfInfo hfAction = iota
	hfTree
	hfFile
)

// hfRequest is a parsed request path.
type hfRequest struct {
	action hfAction
	// kind is the Hub's plural: models, datasets or spaces.
	kind string
	// id is the repository id as the client typed it ("gpt2", "org/name").
	id string
	// rev is the revision: a branch, a tag, refs/pr/N or a commit sha. Empty
	// for repository info without revision.
	rev string
	// path is the file (hfFile) or the directory (hfTree) below the revision.
	path string
}

// idKey is the id the cache and the curation rules know the repository by: the
// Hub resolves ids case-insensitively.
func (q hfRequest) idKey() string { return strings.ToLower(q.id) }

// pkg is the curation package name: the id, prefixed for datasets and spaces.
func (q hfRequest) pkg() string {
	switch q.kind {
	case "datasets", "spaces":
		return q.kind + "/" + q.idKey()
	}

	return q.idKey()
}

// hfKinds maps the URL prefixes of the non-model repositories to the Hub's
// plural kind.
var hfKinds = map[string]string{"datasets": "datasets", "spaces": "spaces"} //nolint:gochecknoglobals // constant lookup table

// hfEscapedRel is rel as the client escaped it. The dispatcher hands over the
// decoded path, in which the slashes of a revision such as refs%2Fpr%2F1 are
// indistinguishable from path separators.
func hfEscapedRel(r *http.Request, rel string) string {
	esc := r.URL.EscapedPath()

	for i := range len(esc) {
		if esc[i] != '/' {
			continue
		}

		if u, err := url.PathUnescape(esc[i:]); err == nil && u == "/"+rel {
			return esc[i+1:]
		}
	}

	return rel
}

// parseHF parses the escaped repository-relative path of a request.
func parseHF(escaped string) (hfRequest, bool) {
	if escaped == "" || len(escaped) > maxPathLen {
		return hfRequest{}, false
	}

	raw := strings.Split(escaped, "/")
	parts := make([]string, len(raw))

	for i, p := range raw {
		u, err := url.PathUnescape(p)
		if err != nil || u == "" && i < len(raw)-1 {
			return hfRequest{}, false
		}

		parts[i] = u
	}

	if parts[0] == "api" {
		return parseHFAPI(parts[1:])
	}

	kind := "models"

	if k, ok := hfKinds[parts[0]]; ok {
		kind, parts = k, parts[1:]
	}

	// The id is one or two segments, then "resolve", the revision, the path.
	for j := 1; j <= 2 && j+2 < len(parts); j++ {
		if parts[j] != "resolve" {
			continue
		}

		q := hfRequest{action: hfFile, kind: kind, id: strings.Join(parts[:j], "/"), rev: parts[j+1], path: strings.Join(parts[j+2:], "/")}

		return q, q.valid() && q.path != "" && pathseg.Path(q.path)
	}

	return hfRequest{}, false
}

func parseHFAPI(parts []string) (hfRequest, bool) {
	if len(parts) < 2 || hfKinds[parts[0]] == "" && parts[0] != "models" {
		return hfRequest{}, false
	}

	kind, rest := parts[0], parts[1:]
	q := hfRequest{action: hfInfo, kind: kind}

	for j := 1; j <= 2 && j < len(rest); j++ {
		if rest[j] != "revision" && rest[j] != "tree" {
			continue
		}

		q.id = strings.Join(rest[:j], "/")
		tail := rest[j+1:]

		if len(tail) == 0 || tail[0] == "" {
			return hfRequest{}, false
		}

		q.rev = tail[0]

		if rest[j] == "revision" {
			return q, len(tail) == 1 && q.valid()
		}

		q.action = hfTree
		q.path = strings.Join(tail[1:], "/")

		return q, q.valid() && (q.path == "" || pathseg.Path(q.path))
	}

	if len(rest) > 2 {
		return hfRequest{}, false
	}

	q.id = strings.Join(rest, "/")

	return q, q.valid()
}

// valid checks the id and the revision: they are put into upstream URLs and
// cache keys.
func (q hfRequest) valid() bool {
	ids := strings.Split(q.id, "/")
	if len(ids) > 2 {
		return false
	}

	for _, p := range ids {
		if len(p) > hfMaxIDPartLen || !hfIDPart.MatchString(p) {
			return false
		}
	}

	if q.rev == "" {
		return true
	}

	return len(q.rev) <= hfMaxRevLen && hfRevOK.MatchString(q.rev) &&
		!strings.Contains(q.rev, "..") && !strings.Contains(q.rev, "//") && !strings.HasSuffix(q.rev, "/")
}

// hfDenied is an upstream refusal (401 or 403) with the error code and message
// the client reads to tell a gated repository from a missing one.
type hfDeniedError struct {
	status    int
	code, msg string
	err       error
}

func (e *hfDeniedError) Error() string {
	return fmt.Sprintf("upstream refused: %d %s", e.status, e.code)
}
func (e *hfDeniedError) Unwrap() error { return e.err }

var (
	errHFDocument = errors.New("not the expected upstream document")
	errHFMismatch = errors.New("file does not match what the upstream announced")
)

func hfHeader(h http.Header, name string) string {
	v := h.Get(name)
	if len(v) > 512 {
		v = v[:512]
	}

	return v
}

// hfRefuse is fetchOpts.refuse: it keeps what the upstream said about a refusal.
func hfRefuse(resp *http.Response, err error) error {
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return nil
	}

	return &hfDeniedError{
		status: resp.StatusCode,
		code:   hfHeader(resp.Header, "X-Error-Code"),
		msg:    hfHeader(resp.Header, "X-Error-Message"),
		err:    err,
	}
}

// hfCall is one request being served.
type hfCall struct {
	s    *Service
	repo *models.Repository
	req  hfRequest
	g    versionGate
	auth string
}

func (s *Service) serveHuggingFace(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	req, ok := parseHF(hfEscapedRel(r, relPath))
	if !ok {
		http.NotFound(w, r)
		return
	}

	c := &hfCall{s: s, repo: repo, req: req}

	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	c.g = g

	if repo.UpstreamPasswordEnc != "" {
		token, derr := crypto.Decrypt(repo.UpstreamPasswordEnc)
		if derr != nil {
			s.log.Errorw("failed to decrypt proxy upstream token", "repo", repo.Name, "error", derr)
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		c.auth = "Bearer " + token
	}

	switch req.action {
	case hfInfo:
		c.serveInfo(w, r)
	case hfTree:
		c.serveTree(w, r)
	case hfFile:
		c.serveFile(w, r)
	}
}

// opts completes the fetch options of an upstream request: the token and the
// capture of refusals.
func (c *hfCall) opts(o fetchOpts) fetchOpts {
	o.authorization = c.auth
	o.refuse = hfRefuse

	return o
}

// fail answers err the way the client expects: with the error code it maps to
// an exception. missing names the code of a 404 at this step.
func (c *hfCall) fail(w http.ResponseWriter, r *http.Request, err error, missing string) {
	var denied *hfDeniedError

	switch {
	case errors.As(err, &denied):
		h := w.Header()

		if denied.code != "" {
			h.Set("X-Error-Code", denied.code)
		}

		msg := denied.msg
		if msg == "" {
			msg = http.StatusText(denied.status)
		}

		h.Set("X-Error-Message", msg)
		http.Error(w, msg, denied.status)
	case errors.Is(err, errNotFound):
		w.Header().Set("X-Error-Code", missing)
		http.Error(w, "not found", http.StatusNotFound)
	default:
		c.s.writeResolveError(w, r, err)
	}
}

// ---- upstream URLs and documents ----

func (c *hfCall) idPath() string {
	parts := strings.Split(c.req.id, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}

	return strings.Join(parts, "/")
}

func (c *hfCall) keyPrefix(dir string) string {
	return dir + c.req.kind + "/" + c.req.idKey() + "/"
}

func hfJoin(base, rel string) string { return strings.TrimRight(base, "/") + "/" + rel }

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}

	return strings.Join(parts, "/")
}

func (c *hfCall) apiURL(rest string) func(string) (string, error) {
	return func(upstream string) (string, error) {
		return hfJoin(upstream, "api/"+c.req.kind+"/"+c.idPath()+rest), nil
	}
}

// fileURL is where the file of the commit sha is downloaded from.
func (c *hfCall) fileURL(upstream, sha, p string) string {
	prefix := ""
	if c.req.kind != "models" {
		prefix = c.req.kind + "/"
	}

	return hfJoin(upstream, prefix+c.idPath()+"/resolve/"+url.PathEscape(sha)+"/"+escapePath(p))
}

func hfVerifyJSON(object bool) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		body, err := io.ReadAll(io.LimitReader(spool, hfDocLimit+1))
		if err != nil {
			return err
		}

		b := bytes.TrimSpace(body)
		if len(b) == 0 || (object && b[0] != '{') || (!object && b[0] != '[') || !json.Valid(b) {
			return errHFDocument
		}

		return nil
	}
}

// infoDoc is the repository info of rev ("" for the default branch); blobs asks
// for the variant with file sizes.
func (c *hfCall) infoDoc(rev string, blobs bool) metaDoc {
	dir, rest, seg := hfInfoDir, "", hfDefaultRev

	if rev != "" {
		rest, seg = "/revision/"+url.PathEscape(rev), url.PathEscape(rev)
	}

	if blobs {
		dir, rest = hfBlobsDir, rest+"?blobs=true"
	}

	return metaDoc{
		key:   c.keyPrefix(dir) + seg,
		url:   c.apiURL(rest),
		opts:  staticOpts(c.opts(jsonDocOpts(hfVerifyJSON(true)))),
		limit: hfDocLimit,
		what:  "huggingface repository info",
		bad:   badDocDrop,
	}
}

func (c *hfCall) commitsDoc(sha string, page int) metaDoc {
	return metaDoc{
		key:   c.keyPrefix(hfCommitsDir) + sha + "/" + strconv.Itoa(page),
		url:   c.apiURL("/commits/" + url.PathEscape(sha) + "?p=" + strconv.Itoa(page)),
		opts:  staticOpts(c.opts(jsonDocOpts(hfVerifyJSON(false)))),
		limit: hfDocLimit,
		what:  "huggingface commit history",
		bad:   badDocDrop,
	}
}

type hfInfoBody struct {
	SHA          string `json:"sha"`
	LastModified string `json:"lastModified"` //nolint:tagliatelle // the Hub's field name
}

func (b hfInfoBody) date() time.Time {
	t, _ := time.Parse(time.RFC3339, b.LastModified)
	return t
}

type hfCommit struct {
	ID   string `json:"id"`
	Date string `json:"date"`
}

func (cm hfCommit) date() time.Time {
	t, _ := time.Parse(time.RFC3339, cm.Date)
	return t
}

func (c *hfCall) info(ctx context.Context, rev string) (hfInfoBody, *resolution, error) {
	return parseDoc(ctx, c.s, c.repo, c.infoDoc(rev, false), func(body []byte, _ *resolution) (hfInfoBody, error) {
		var b hfInfoBody
		if err := json.Unmarshal(body, &b); err != nil {
			return b, err
		}

		b.SHA = strings.ToLower(b.SHA)
		if !hfSHA1Hex.MatchString(b.SHA) {
			return b, errHFDocument
		}

		return b, nil
	})
}

func (c *hfCall) commits(ctx context.Context, head string, page int) ([]hfCommit, error) {
	cms, _, err := parseDoc(ctx, c.s, c.repo, c.commitsDoc(head, page), func(body []byte, _ *resolution) ([]hfCommit, error) {
		var out []hfCommit
		return out, json.Unmarshal(body, &out)
	})

	return cms, err
}

// ---- revision pinning ----

// hfPin is the commit a request is answered from.
type hfPin struct {
	sha string
	// ref is the info of the ref the request named, nil for a commit sha.
	ref *hfInfoBody
	res *resolution
}

// pin resolves the revision of the request to a commit the gate lets through:
// a commit sha is checked as it is; a ref is looked up (the lookup is cached for
// the repository's TTL) and, when its head is held back by the minimum age or
// the curation rules, moved back to the newest commit that is not.
func (c *hfCall) pin(ctx context.Context) (hfPin, error) {
	rev := c.req.rev

	if hfSHA1Hex.MatchString(strings.ToLower(rev)) {
		sha := strings.ToLower(rev)

		var date time.Time

		if c.g.AgeActive() {
			b, _, err := c.info(ctx, sha)
			if err != nil {
				return hfPin{}, err
			}

			date = b.date()
		}

		return hfPin{sha: sha}, checkGate(c.g, c.req.pkg(), sha, date)
	}

	b, res, err := c.info(ctx, rev)
	if err != nil {
		return hfPin{}, err
	}

	pin := hfPin{sha: b.SHA, ref: &b, res: res}

	held := checkGate(c.g, c.req.pkg(), b.SHA, b.date())
	if held == nil {
		return pin, nil
	}

	for page := range hfMaxPages {
		cms, cerr := c.commits(ctx, b.SHA, page)
		if cerr != nil {
			return hfPin{}, cerr
		}

		for _, cm := range cms {
			sha := strings.ToLower(cm.ID)
			if hfSHA1Hex.MatchString(sha) && checkGate(c.g, c.req.pkg(), sha, cm.date()) == nil {
				return hfPin{sha: sha}, nil
			}
		}

		if len(cms) < hfCommitsPage {
			break
		}
	}

	return hfPin{}, held
}

func (c *hfCall) missingRev() string {
	if c.req.rev == "" {
		return "RepoNotFound"
	}

	return "RevisionNotFound"
}

// ---- repository info ----

func (c *hfCall) serveInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pin, err := c.pin(ctx)
	if err != nil {
		c.fail(w, r, err, c.missingRev())
		return
	}

	blobs := r.URL.Query().Get("blobs") == "true"

	var (
		res  *resolution
		body []byte
		done func()
	)

	if pin.ref != nil && !blobs && pin.ref.SHA == pin.sha {
		res, body, done, err = c.s.readDoc(ctx, c.repo, c.infoDoc(c.req.rev, false))
	} else {
		res, body, done, err = c.s.readDoc(ctx, c.repo, c.infoDoc(pin.sha, blobs))
	}

	if err != nil {
		c.fail(w, r, err, "RevisionNotFound")
		return
	}
	defer done()

	w.Header().Set("X-Repo-Commit", pin.sha)
	c.s.serveGeneratedJSON(w, r, c.repo, res, body, 0)
}

// ---- tree listings ----

type hfTreeDoc struct {
	Next    string          `json:"next,omitempty"`
	Entries json.RawMessage `json:"entries"`
}

// treeTransform caches a listing page as hfTreeDoc: the cursor of the next page
// (which the upstream gives in a header, that is not cached) and the entries
// without their Xet hashes, which would make a client skip the HEAD request and
// talk to the Xet servers.
func (c *hfCall) treeTransform(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, hfDocLimit+1))
	if err != nil || len(body) > hfDocLimit {
		return errUpstream
	}

	var entries []map[string]json.RawMessage
	if json.Unmarshal(body, &entries) != nil {
		return errUpstream
	}

	for _, e := range entries {
		delete(e, "xetHash")
	}

	stripped, err := json.Marshal(entries)
	if err != nil {
		return errUpstream
	}

	doc := hfTreeDoc{Entries: stripped}

	if m := hfNextLink.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
		if u, perr := url.Parse(m[1]); perr == nil {
			doc.Next = u.Query().Get("cursor")
		}
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return errUpstream
	}

	replaceBody(resp, out)

	return nil
}

func (c *hfCall) serveTree(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pin, err := c.pin(ctx)
	if err != nil {
		c.fail(w, r, err, c.missingRev())
		return
	}

	q := r.URL.Query()
	flag := func(name string) string {
		if v := q.Get(name); v == "true" || v == "1" {
			return "true"
		}

		return "false"
	}

	args := url.Values{"recursive": {flag("recursive")}, "expand": {flag("expand")}}

	if n, perr := strconv.Atoi(q.Get("limit")); perr == nil && n > 0 {
		args.Set("limit", strconv.Itoa(min(n, hfMaxLimit)))
	}

	if cur := q.Get("cursor"); cur != "" {
		if len(cur) > 2048 || !hfCursorOK.MatchString(cur) {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}

		args.Set("cursor", cur)
	}

	rest := "/tree/" + url.PathEscape(pin.sha)
	if c.req.path != "" {
		rest += "/" + escapePath(c.req.path)
	}

	d := metaDoc{
		key:   c.keyPrefix(hfTreeDir) + pin.sha + "/" + url.QueryEscape(args.Encode()) + "/" + c.req.path,
		url:   c.apiURL(rest + "?" + args.Encode()),
		limit: hfDocLimit,
		what:  "huggingface tree listing",
		bad:   badDocDrop,
	}
	d.opts = staticOpts(c.opts(fetchOpts{
		accept: jsonMediaType, contentType: jsonMediaType, transform: c.treeTransform,
	}))

	res, body, done, err := c.s.readDoc(ctx, c.repo, d)
	if err != nil {
		c.fail(w, r, err, "EntryNotFound")
		return
	}
	defer done()

	var doc hfTreeDoc
	if json.Unmarshal(body, &doc) != nil {
		c.s.dropUnusable(ctx, c.repo, d, errHFDocument)
		http.Error(w, "bad upstream document", http.StatusBadGateway)

		return
	}

	entries := bytes.Clone(doc.Entries)

	if doc.Next != "" {
		next := url.Values{}
		maps.Copy(next, q)

		next.Set("cursor", doc.Next)
		w.Header().Add("Link", "<"+c.s.publicBase(w, r)+r.URL.EscapedPath()+"?"+next.Encode()+`>; rel="next"`)
	}

	w.Header().Set("X-Repo-Commit", pin.sha)
	c.s.serveGeneratedJSON(w, r, c.repo, res, entries, 0)
}

// ---- files ----

// hfFileMeta is what the upstream's HEAD says about a file of a commit.
type hfFileMeta struct {
	ETag string `json:"etag"`
	Size int64  `json:"size"`
}

func (c *hfCall) metaKey(sha string) string {
	return c.keyPrefix(hfMetaDir) + sha + "/" + c.req.path
}

func (c *hfCall) fileKey(sha string) string {
	return c.keyPrefix(hfFileDir) + sha + "/" + c.req.path
}

func hfTrimETag(v string) string {
	v = strings.TrimPrefix(v, "W/")
	return strings.ToLower(strings.Trim(v, `"`))
}

// fetchMeta asks the upstream, with a HEAD that does not follow redirects, for
// the commit, the validator and the size of a file: the Hub answers them in the
// headers of the redirect to its storage.
func (c *hfCall) fetchMeta(sha string) fetchFunc {
	key := c.metaKey(sha)

	return func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		s, repo := c.s, c.repo
		target := c.fileURL(repo.UpstreamURL, sha, c.req.path)

		if verr := netutil.ValidateOutboundURLContext(fctx, target); verr != nil {
			s.log.Warnw("proxy upstream rejected by outbound URL policy", "repo", repo.Name, "error", verr)
			return nil, outboundError(verr)
		}

		req, err := s.newUpstreamRequest(fctx, repo, target, c.opts(fetchOpts{}))
		if err != nil {
			return nil, err
		}

		req.Method = http.MethodHead

		if aerr := s.acquireFetch(fctx, repo, key); aerr != nil {
			return nil, aerr
		}
		defer s.fetches.Release()

		client := *s.client
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

		resp, err := client.Do(req)
		if err != nil {
			s.log.Warnw("proxy upstream request failed", "repo", repo.Name, "path", key, "error", err)
			return nil, errUpstreamDown
		}

		defer func() { base.CloseLog(resp.Body, s.log) }()

		redirect := resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified

		if resp.StatusCode != http.StatusOK && !redirect {
			serr := s.upstreamStatusError(repo, key, resp.StatusCode)
			if errors.Is(serr, errNotFound) {
				s.recordNegative(fctx, repo, key)
			}

			if rerr := hfRefuse(resp, serr); rerr != nil {
				return nil, rerr
			}

			return nil, serr
		}

		if commit := resp.Header.Get("X-Repo-Commit"); commit != "" && !strings.EqualFold(commit, sha) {
			s.log.Warnw("proxy upstream answered another commit", "repo", repo.Name, "path", key, "commit", commit)
			return nil, errVerify
		}

		m := hfFileMeta{ETag: hfTrimETag(cmpOr(resp.Header.Get("X-Linked-Etag"), resp.Header.Get("ETag"))), Size: -1}

		if v := resp.Header.Get("X-Linked-Size"); v != "" {
			if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
				m.Size = n
			}
		} else if !redirect && resp.ContentLength >= 0 {
			m.Size = resp.ContentLength
		}

		if m.Size < 0 {
			s.log.Warnw("proxy upstream gave no file size", "repo", repo.Name, "path", key)
			return nil, errVerify
		}

		body, err := json.Marshal(m)
		if err != nil {
			return nil, errInternal
		}

		digest := sha256.Sum256(body)
		sum := hex.EncodeToString(digest[:])
		now := time.Now()

		entry := &models.ProxyCacheEntry{
			RepositoryID: repo.ID,
			CacheKey:     key,
			BlobKey:      sum,
			ContentType:  jsonMediaType,
			StatusCode:   http.StatusOK,
			SizeBytes:    int64(len(body)),
			ExpiresAt:    now.Add(immutableTTL),
			CreatedAt:    now,
			UpdatedAt:    now,
		}

		if perr := s.persist(fctx, bytes.NewReader(body), sum, jsonMediaType, entry); perr != nil {
			s.log.Errorw("failed to cache file metadata", "repo", repo.Name, "path", key, "error", perr)
			return nil, errInternal
		}

		return entry, nil
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

func (c *hfCall) fileMeta(ctx context.Context, sha string) (hfFileMeta, error) {
	key := c.metaKey(sha)
	fetch := c.fetchMeta(sha)

	res, err := c.s.resolveWith(ctx, c.repo, key, fetch)
	if err != nil {
		return hfFileMeta{}, err
	}

	_, body, release, err := c.s.loadCachedBody(ctx, c.repo, key, res, 4096, "huggingface file metadata",
		func() (*resolution, error) { return c.s.resolveWith(ctx, c.repo, key, fetch) })
	if err != nil {
		return hfFileMeta{}, err
	}
	defer release()

	var m hfFileMeta
	if json.Unmarshal(body, &m) != nil {
		return hfFileMeta{}, errInternal
	}

	return m, nil
}

// fileOpts are the checks of the file's body: its size, and the digest the etag
// carries (sha256 of an LFS file, the git blob id of any other).
func (c *hfCall) fileOpts(m hfFileMeta) fetchOpts {
	o := c.opts(fetchOpts{})

	switch {
	case hfSHA256.MatchString(m.ETag):
		o.wantSHA256 = m.ETag
	case hfSHA1Hex.MatchString(m.ETag):
	default:
		o.unverified = true
	}

	o.verify = func(spool io.ReadSeeker) error {
		n, err := spool.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}

		if n != m.Size {
			return fmt.Errorf("%w: %d bytes, the upstream announced %d", errHFMismatch, n, m.Size)
		}

		if !hfSHA1Hex.MatchString(m.ETag) {
			return nil
		}

		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			return err
		}

		h := sha1.New() //nolint:gosec // see the import
		_, _ = fmt.Fprintf(h, "blob %d\x00", n)

		if _, err := iocopy.Copy(h, spool); err != nil {
			return err
		}

		if got := hex.EncodeToString(h.Sum(nil)); got != m.ETag {
			return fmt.Errorf("%w: git blob id %s, the upstream announced %s", errHFMismatch, got, m.ETag)
		}

		return nil
	}

	return o
}

func (c *hfCall) serveFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pin, err := c.pin(ctx)
	if err != nil {
		c.fail(w, r, err, c.missingRev())
		return
	}

	m, err := c.fileMeta(ctx, pin.sha)
	if err != nil {
		c.fail(w, r, err, "EntryNotFound")
		return
	}

	h := w.Header()
	h.Set("X-Repo-Commit", pin.sha)

	if m.ETag != "" {
		h.Set("X-Linked-Etag", `"`+m.ETag+`"`)
	}

	h.Set("X-Linked-Size", strconv.FormatInt(m.Size, 10))

	if r.Method == http.MethodHead {
		if m.ETag != "" {
			h.Set("ETag", `"`+m.ETag+`"`)
		}

		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Length", strconv.FormatInt(m.Size, 10))
		w.WriteHeader(http.StatusOK)

		return
	}

	key := c.fileKey(pin.sha)
	fetch := func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		return c.s.fetchURL(fctx, c.repo, key, c.fileURL(c.repo.UpstreamURL, pin.sha, c.req.path), c.fileOpts(m))
	}
	resolve := func() (*resolution, error) { return c.s.resolveWith(ctx, c.repo, key, fetch) }

	res, err := resolve()
	if err != nil {
		c.fail(w, r, err, "EntryNotFound")
		return
	}

	c.s.serveResolved(w, r, c.repo, key, res, resolve)
}

// ---- hooks ----

// HuggingFaceHooks is the proxy hooks of the huggingface format.
var HuggingFaceHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve:     (*Service).serveHuggingFace,
	Immutable: hfImmutable,
	// Everything but the files themselves is a small document.
	IsMetadata: func(key string) bool { return !strings.HasPrefix(key, hfFileDir) },
	Published:  (*Service).hfPublished,
	Versions:   (*Service).hfVersions,
	BlobPath:   presign.HuggingFaceBlobPath,
}

// hfImmutable: files, their metadata, commit histories and tree listings are
// keyed by commit sha and never change; the info of a ref changes, the info of
// a commit does not.
func hfImmutable(key string) bool {
	switch {
	case strings.HasPrefix(key, hfFileDir), strings.HasPrefix(key, hfMetaDir),
		strings.HasPrefix(key, hfCommitsDir), strings.HasPrefix(key, hfTreeDir):
		return true
	case strings.HasPrefix(key, hfInfoDir), strings.HasPrefix(key, hfBlobsDir):
		return hfSHA1Hex.MatchString(path.Base(key))
	}

	return false
}

// hfSplitPackage splits a curation package name into the request it stands for.
func hfSplitPackage(pkg string) hfRequest {
	q := hfRequest{kind: "models", id: pkg}

	for _, k := range []string{"datasets", "spaces"} {
		if rest, ok := strings.CutPrefix(pkg, k+"/"); ok {
			q = hfRequest{kind: k, id: rest}
		}
	}

	return q
}

// hfPublished is Hooks.Published: the date of a commit, from the cached
// repository info of the commit or of a ref that points at it, or from a cached
// page of the history.
func (s *Service) hfPublished(ctx context.Context, repo *models.Repository, pkg, sha string) (time.Time, bool) {
	c := &hfCall{s: s, repo: repo, req: hfSplitPackage(pkg)}

	if body, ok := s.cachedDocBody(ctx, repo, c.infoDoc(sha, false)); ok {
		var b hfInfoBody
		if json.Unmarshal(body, &b) == nil && !b.date().IsZero() {
			return b.date(), true
		}
	}

	for _, v := range c.cachedVersions(ctx) {
		if v.Version == sha {
			return v.Published, !v.Published.IsZero()
		}
	}

	return time.Time{}, false
}

// hfVersions is Hooks.Versions: the commits of the cached history of the
// default branch, newest first, or the head alone when no history was needed.
func (s *Service) hfVersions(ctx context.Context, repo *models.Repository, pkg string) ([]DatedVersion, bool) {
	c := &hfCall{s: s, repo: repo, req: hfSplitPackage(pkg)}

	vs := c.cachedVersions(ctx)

	return vs, len(vs) > 0
}

// cachedVersions lists, from the cache alone, the head of the default branch and
// the commits of every cached history page that ends in it.
func (c *hfCall) cachedVersions(ctx context.Context) []DatedVersion {
	body, ok := c.s.cachedDocBody(ctx, c.repo, c.infoDoc("", false))
	if !ok {
		return nil
	}

	var head hfInfoBody
	if json.Unmarshal(body, &head) != nil || !hfSHA1Hex.MatchString(strings.ToLower(head.SHA)) {
		return nil
	}

	sha := strings.ToLower(head.SHA)
	out := []DatedVersion{{Version: sha, Published: head.date()}}

	for page := range hfMaxPages {
		pb, found := c.s.cachedDocBody(ctx, c.repo, c.commitsDoc(sha, page))
		if !found {
			break
		}

		var cms []hfCommit
		if json.Unmarshal(pb, &cms) != nil {
			break
		}

		for _, cm := range cms {
			if id := strings.ToLower(cm.ID); id != sha {
				out = append(out, DatedVersion{Version: id, Published: cm.date()})
			}
		}

		if len(cms) < hfCommitsPage {
			break
		}
	}

	return out
}
