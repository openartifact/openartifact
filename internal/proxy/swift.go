// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
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
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/pkg/swift"
)

// Swift proxy repositories mirror a Swift package registry (SE-0292, SE-0391);
// the upstream URL is the registry root, e.g. https://registry.example or
// another OpenArtifact's http://host/repos/swift/NAME. Clients use
// HOST/repos/swift/NAME as their registry URL. Every URL and Link a client sees
// points at this repository: the upstream's own are never handed out.
//
// The cache keeps one body and content type per key, so the answers that carry
// more than a body are normalised into JSON envelopes while they are fetched
// (Fetch's transform stage) and rebuilt, with our links, when served:
//
//   - "<scope>/<name>": the release list, as {"releases":{"<version>":{}}}. The
//     list answered is regenerated from it: newest first, release URLs of this
//     repository, and the latest-version link recomputed over the versions the
//     version gate leaves.
//   - "<scope>/<name>/<version>": the release document verbatim, with the
//     latest, successor and predecessor links of the upstream that point into
//     this package reduced to their versions.
//   - "<scope>/<name>/<version>/Package.swift": the manifest and the
//     alternate manifests (Link rel="alternate") the upstream lists for it.
//     "<scope>/<name>/<version>/Package@swift-X.swift" is an alternate
//     manifest, fetched with ?swift-version=X when asked for. An unknown
//     swift-version answers 303 to the plain manifest, like the hosted repository.
//   - "<scope>/<name>/<version>.zip": the source archive, immutable, cached
//     only when its sha256 equals the checksum of the release document. It is
//     answered with the Digest and Content-Disposition headers of the protocol.
//   - "-/identifiers/<sha256 of the url>": the identifiers answer for a
//     repository URL, verbatim.
//
// Scope and name compare case-insensitively: the cache keys are lower case.
//
// The version gate (see gate.go) hides curated and too young versions from the
// list and refuses their release, manifest and archive. The minimum age is read
// from the "publishedAt" of the release document; the list looks at the newest
// swiftGateProbes of them until it meets one that is old enough.
//
// A proxy repository is read-only: PUT and DELETE answer 405 (problem+json), and
// so does POST /login. The protocol's 501 ("login is not supported") is not
// used, to answer every write the same way as the other read-only repositories.
// Every error answer is problem+json; the plain-text errors of the shared
// pipeline are converted by swiftProblemWriter.
//
// Presigned redirects are not used for archives: the Digest header has to
// accompany the bytes.

var (
	errSwiftNotAlternate = errors.New("response is not the version-specific manifest")
	errSwiftIdentifier   = errors.New("invalid identifier")
)

const (
	// swiftMaxAge is how long a generated document may be reused downstream.
	swiftMaxAge = 60
	// swiftMaxManifest caps a manifest read from the upstream.
	swiftMaxManifest = 4 << 20
	// swiftMaxAlternates caps the alternate manifests kept per release.
	swiftMaxAlternates = 64
	// swiftMaxProblem caps the text of a pipeline error kept as a problem detail.
	swiftMaxProblem = 512
	// swiftGateProbes bounds the release documents the list reads to date the
	// newest versions for the minimum age.
	swiftGateProbes = 32

	swiftArchiveExt = ".zip"
	swiftIdentsDir  = "-/identifiers/"
)

// SwiftHooks wires the swift proxy.
//
//nolint:gochecknoglobals // format hook table
var SwiftHooks = Hooks{
	Serve:            (*Service).serveSwift,
	MethodNotAllowed: swiftReadOnly,
	WriteEntry:       (*Service).writeSwiftEntry,
	Coordinates:      swiftCoordinates,
	Published:        (*Service).swiftPublishedAt,
	Versions:         (*Service).swiftVersions,
	Immutable:        func(key string) bool { return strings.HasSuffix(key, swiftArchiveExt) },
	IsMetadata:       func(key string) bool { return !strings.HasSuffix(key, swiftArchiveExt) },
}

// swiftReadOnly answers a write to a proxy repository.
func swiftReadOnly(w http.ResponseWriter) {
	swift.WriteProblem(w, http.StatusMethodNotAllowed, "proxy repositories are read-only")
}

// swiftCoordinates is Hooks.Coordinates: the package ("scope.name", lower case)
// and version a release, manifest or archive path is about.
func swiftCoordinates(rel string) (string, string, bool) {
	p, ok := swift.ParsePath(rel)
	if !ok {
		return "", "", false
	}

	switch p.Kind {
	case swift.KindRelease, swift.KindManifest, swift.KindArchive:
		return swiftPackage(p), p.Version, true
	default:
		return "", "", false
	}
}

func swiftPackage(p swift.Path) string { return swift.Key(p.Scope, p.Name) }

func swiftPkgKey(p swift.Path) string {
	return strings.ToLower(p.Scope) + "/" + strings.ToLower(p.Name)
}

func swiftReleaseKey(p swift.Path) string { return swiftPkgKey(p) + "/" + p.Version }

func swiftManifestKey(p swift.Path) string { return swiftReleaseKey(p) + "/" + swift.ManifestFile }

func swiftArchiveKey(p swift.Path) string { return swiftReleaseKey(p) + swiftArchiveExt }

// swiftURL is the public URL of repo's package, or of its release when version
// is not empty.
func swiftURL(origin string, repo *models.Repository, p swift.Path, version string) string {
	u := origin + "/repos/swift/" + url.PathEscape(repo.Name) + "/" + p.Scope + "/" + p.Name
	if version != "" {
		u += "/" + version
	}

	return u
}

// swiftProblemWriter turns the plain-text error answers of the shared pipeline
// (http.Error, http.NotFound) into problem+json documents. Answers that are not
// plain-text errors, the JSON refusals of the version gate and of quarantine
// among them, pass through.
type swiftProblemWriter struct {
	http.ResponseWriter

	code   int
	detail []byte
}

func (p *swiftProblemWriter) WriteHeader(code int) {
	if code >= http.StatusBadRequest && strings.HasPrefix(p.Header().Get("Content-Type"), "text/plain") {
		p.code = code

		return
	}

	p.ResponseWriter.WriteHeader(code)
}

func (p *swiftProblemWriter) Write(b []byte) (int, error) {
	if p.code == 0 {
		return p.ResponseWriter.Write(b)
	}

	p.detail = append(p.detail, b[:min(len(b), swiftMaxProblem-min(len(p.detail), swiftMaxProblem))]...)

	return len(b), nil
}

func (p *swiftProblemWriter) Flush() {
	if f, ok := p.ResponseWriter.(http.Flusher); ok && p.code == 0 {
		f.Flush()
	}
}

func (p *swiftProblemWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }

// finish writes the problem document of a converted error.
func (p *swiftProblemWriter) finish() {
	if p.code == 0 {
		return
	}

	code := p.code
	p.code = 0

	h := p.Header()
	h.Del("Digest")
	h.Del("Content-Disposition")
	h.Del("Content-Length")

	swift.WriteProblem(p.ResponseWriter, code, cmp.Or(strings.TrimSpace(string(p.detail)), http.StatusText(code)))
}

func (s *Service) serveSwift(w http.ResponseWriter, r *http.Request, repo *models.Repository, rel string) {
	if !swift.CheckRequest(w, r) {
		return
	}

	pw := &swiftProblemWriter{ResponseWriter: w}
	defer pw.finish()

	p, ok := swift.ParsePath(rel)
	if !ok {
		http.NotFound(pw, r)
		return
	}

	switch p.Kind {
	case swift.KindList:
		s.serveSwiftList(pw, r, repo, p)
	case swift.KindRelease:
		s.serveSwiftRelease(pw, r, repo, p)
	case swift.KindManifest:
		s.serveSwiftManifest(pw, r, repo, p)
	case swift.KindArchive:
		s.serveSwiftArchive(pw, r, repo, p)
	case swift.KindIdentifiers:
		s.serveSwiftIdentifiers(pw, r, repo)
	default:
		// Login is a POST, refused before a hook runs; a GET names nothing.
		http.NotFound(pw, r)
	}
}

// swiftBody reads the upstream document of a transform, at most limit bytes.
func (s *Service) swiftBody(repo *models.Repository, resp *http.Response, limit int64, what string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		s.log.Warnw("failed to read upstream "+what, "repo", repo.Name, "error", err)
		return nil, errUpstreamDown
	}

	if int64(len(data)) > limit {
		s.log.Warnw("upstream "+what+" too large", "repo", repo.Name, "limit", limit)
		return nil, errUpstream
	}

	return data, nil
}

// swiftRefuse logs an upstream document the transform rejects.
func (s *Service) swiftRefuse(repo *models.Repository, what string, err error) error {
	s.log.Warnw("unusable upstream "+what, "repo", repo.Name, "error", err)

	return errVerify
}

// swiftOpts are the fetch options of a document cached as a JSON envelope the
// transform builds from the upstream's answer.
func swiftOpts(accept string, transform func(*http.Response) error) func(context.Context, string) fetchOpts {
	return staticOpts(fetchOpts{accept: accept, contentType: jsonMediaType, transform: transform})
}

// ---- release list ----

func (s *Service) swiftListDoc(repo *models.Repository, p swift.Path) metaDoc {
	return metaDoc{
		key: swiftPkgKey(p),
		opts: swiftOpts(swift.MediaJSON,
			func(resp *http.Response) error { return s.swiftListTransform(repo, resp) }),
		limit: swift.MaxDocBytes, what: "swift release list", bad: badDocInternal,
	}
}

func (s *Service) swiftListTransform(repo *models.Repository, resp *http.Response) error {
	data, err := s.swiftBody(repo, resp, swift.MaxDocBytes, "release list")
	if err != nil {
		return err
	}

	list, perr := swift.ParseReleaseList(data)
	if perr != nil {
		return s.swiftRefuse(repo, "release list", perr)
	}

	if len(list.Releases) == 0 {
		return errNotFound
	}

	replaceBody(resp, swift.ListBody(list.Versions(), nil))

	return nil
}

// swiftVersionList is the versions of p (newest first) the gate leaves.
func (s *Service) swiftVersionList(
	ctx context.Context, repo *models.Repository, g versionGate, p swift.Path,
) (*resolution, []string, error) {
	versions, res, err := parseDoc(ctx, s, repo, s.swiftListDoc(repo, p), func(body []byte, _ *resolution) ([]string, error) {
		list, err := swift.ParseReleaseList(body)
		if err != nil {
			return nil, fmt.Errorf("parse release list: %w", err)
		}

		return list.Versions(), nil
	})
	if err != nil {
		return nil, nil, err
	}

	kept, err := s.swiftGateWalk(ctx, repo, g, p, versions)
	if err != nil {
		return nil, nil, err
	}

	if len(kept) == 0 {
		return nil, nil, errNotFound
	}

	return res, kept, nil
}

// swiftGateWalk applies the gate to versions (newest first). Curation rules need
// no lookup and judge every version. The minimum age needs the release
// document of a version: they are read until the first version that is old
// enough (everything below it is taken to be), or swiftGateProbes were read. A
// listed version without a document cannot be dated and passes.
func (s *Service) swiftGateWalk(
	ctx context.Context, repo *models.Repository, g versionGate, p swift.Path, versions []string,
) ([]string, error) {
	if !g.Active() {
		return versions, nil
	}

	var (
		pkg     = swiftPackage(p)
		kept    = make([]string, 0, len(versions))
		probing = g.AgeActive()
		probes  int
	)

	for _, v := range versions {
		if hidden, _ := g.Hidden(pkg, v, time.Time{}); hidden {
			continue
		}

		if probing {
			if probes >= swiftGateProbes {
				probing = false
			} else {
				probes++

				pv := p
				pv.Version = v

				_, rel, err := s.swiftReleaseOf(ctx, repo, pv)

				switch {
				case errors.Is(err, errNotFound):
				case err != nil:
					return nil, err
				default:
					published := swiftPublished(rel.doc)
					if hidden, _ := g.Hidden(pkg, v, published); hidden {
						continue
					}

					probing = published.IsZero() || g.Now.Sub(published) < g.MinAge
				}
			}
		}

		kept = append(kept, v)
	}

	return kept, nil
}

func (s *Service) serveSwiftList(w http.ResponseWriter, r *http.Request, repo *models.Repository, p swift.Path) {
	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	res, versions, err := s.swiftVersionList(r.Context(), repo, g, p)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	origin := s.publicBase(w, r)
	body := swift.ListBody(versions, func(v string) string { return swiftURL(origin, repo, p, v) })

	w.Header().Add("Link", swift.Link{URL: swiftURL(origin, repo, p, swift.Latest(versions)), Rel: swift.RelLatest}.String())
	s.serveGeneratedJSON(w, r, repo, res, body, swiftMaxAge)
}

// ---- release ----

// swiftReleaseEnvelope is the cached form of a release document.
type swiftReleaseEnvelope struct {
	Release json.RawMessage    `json:"release"`
	Links   []swiftVersionLink `json:"links,omitempty"`
}

// swiftVersionLink is a latest/successor/predecessor link of the upstream, by
// the version it points at.
type swiftVersionLink struct {
	Rel     string `json:"rel"`
	Version string `json:"version"`
}

// swiftRelease is a parsed cached release document.
type swiftRelease struct {
	doc   swift.Release
	raw   json.RawMessage
	links []swiftVersionLink
}

func (s *Service) swiftReleaseDoc(repo *models.Repository, p swift.Path) metaDoc {
	return metaDoc{
		key: swiftReleaseKey(p),
		opts: swiftOpts(swift.MediaJSON,
			func(resp *http.Response) error { return s.swiftReleaseTransform(repo, p, resp) }),
		limit: swift.MaxDocBytes, what: "swift release", bad: badDocInternal,
	}
}

func (s *Service) swiftReleaseTransform(repo *models.Repository, p swift.Path, resp *http.Response) error {
	data, err := s.swiftBody(repo, resp, swift.MaxDocBytes, "release")
	if err != nil {
		return err
	}

	if _, perr := swift.ParseRelease(data, swift.ID(p.Scope, p.Name), p.Version); perr != nil {
		return s.swiftRefuse(repo, "release", perr)
	}

	var raw bytes.Buffer
	if cerr := json.Compact(&raw, data); cerr != nil {
		return s.swiftRefuse(repo, "release", cerr)
	}

	env := swiftReleaseEnvelope{Release: raw.Bytes()}

	for _, l := range swift.ParseLinks(resp.Header.Values("Link")) {
		if l.Rel != swift.RelLatest && l.Rel != swift.RelSuccessor && l.Rel != swift.RelPredecessor {
			continue
		}

		if t, ok := swift.ParseTarget(l.URL, p.Scope, p.Name); ok && !t.Manifest {
			env.Links = append(env.Links, swiftVersionLink{Rel: l.Rel, Version: t.Version})
		}
	}

	body, merr := json.Marshal(env)
	if merr != nil {
		return s.swiftRefuse(repo, "release", merr)
	}

	replaceBody(resp, body)

	return nil
}

func swiftParseRelease(p swift.Path) func([]byte, *resolution) (*swiftRelease, error) {
	return func(body []byte, _ *resolution) (*swiftRelease, error) {
		var env swiftReleaseEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, fmt.Errorf("parse release envelope: %w", err)
		}

		doc, err := swift.ParseRelease(env.Release, swift.ID(p.Scope, p.Name), p.Version)
		if err != nil {
			return nil, fmt.Errorf("parse release: %w", err)
		}

		return &swiftRelease{doc: doc, raw: env.Release, links: env.Links}, nil
	}
}

func (s *Service) swiftReleaseOf(ctx context.Context, repo *models.Repository, p swift.Path) (*resolution, *swiftRelease, error) {
	rel, res, err := parseDoc(ctx, s, repo, s.swiftReleaseDoc(repo, p), swiftParseRelease(p))

	return res, rel, err
}

// swiftPublished is the publication time a release states, zero when it states
// none.
func swiftPublished(doc swift.Release) time.Time {
	t, err := time.Parse(time.RFC3339, doc.PublishedAt)
	if err != nil {
		return time.Time{}
	}

	return t
}

// swiftAvailable reads the release p and refuses it when the gate holds the
// version back.
func (s *Service) swiftAvailable(
	ctx context.Context, repo *models.Repository, g versionGate, p swift.Path,
) (*resolution, *swiftRelease, error) {
	res, rel, err := s.swiftReleaseOf(ctx, repo, p)
	if err != nil {
		return nil, nil, err
	}

	if err := checkGate(g, swiftPackage(p), p.Version, swiftPublished(rel.doc)); err != nil {
		return nil, nil, err
	}

	return res, rel, nil
}

func (s *Service) serveSwiftRelease(w http.ResponseWriter, r *http.Request, repo *models.Repository, p swift.Path) {
	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	res, rel, err := s.swiftAvailable(r.Context(), repo, g, p)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	origin := s.publicBase(w, r)

	for _, l := range rel.links {
		if checkGate(g, swiftPackage(p), l.Version, time.Time{}) != nil {
			continue
		}

		w.Header().Add("Link", swift.Link{URL: swiftURL(origin, repo, p, l.Version), Rel: l.Rel}.String())
	}

	s.serveGeneratedJSON(w, r, repo, res, rel.raw, swiftMaxAge)
}

// ---- manifest ----

// swiftManifestEnvelope is the cached form of a release's Package.swift.
type swiftManifestEnvelope struct {
	Content    []byte           `json:"content"`
	Alternates []swiftAlternate `json:"alternates,omitempty"`
}

// swiftAlternate is a version-specific manifest the upstream offers.
type swiftAlternate struct {
	SwiftVersion string `json:"swift_version"`
	ToolsVersion string `json:"tools_version,omitempty"`
}

func (s *Service) swiftManifestDoc(repo *models.Repository, p swift.Path) metaDoc {
	return metaDoc{
		key: swiftManifestKey(p),
		opts: swiftOpts(swift.MediaSwift,
			func(resp *http.Response) error { return s.swiftManifestTransform(repo, resp) }),
		limit: swift.MaxDocBytes, what: "swift manifest", bad: badDocInternal,
	}
}

func (s *Service) swiftManifestTransform(repo *models.Repository, resp *http.Response) error {
	data, err := s.swiftBody(repo, resp, swiftMaxManifest, "manifest")
	if err != nil {
		return err
	}

	env := swiftManifestEnvelope{Content: data}

	for _, l := range swift.ParseLinks(resp.Header.Values("Link")) {
		if l.Rel != swift.RelAlternate || len(env.Alternates) >= swiftMaxAlternates {
			continue
		}

		sv, ok := swift.ParseManifestName(l.Params["filename"])
		if !ok || slices.ContainsFunc(env.Alternates, func(a swiftAlternate) bool { return a.SwiftVersion == sv }) {
			continue
		}

		env.Alternates = append(env.Alternates, swiftAlternate{SwiftVersion: sv, ToolsVersion: l.Params["swift-tools-version"]})
	}

	body, merr := json.Marshal(env)
	if merr != nil {
		return s.swiftRefuse(repo, "manifest", merr)
	}

	replaceBody(resp, body)

	return nil
}

func (s *Service) swiftManifestOf(ctx context.Context, repo *models.Repository, p swift.Path) (*resolution, *swiftManifestEnvelope, error) {
	env, res, err := parseDoc(ctx, s, repo, s.swiftManifestDoc(repo, p), func(body []byte, _ *resolution) (*swiftManifestEnvelope, error) {
		var env swiftManifestEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, fmt.Errorf("parse manifest envelope: %w", err)
		}

		return &env, nil
	})

	return res, env, err
}

// swiftAlternateDoc is the manifest of Swift version sv, fetched with
// ?swift-version=sv and cached as it came.
func (s *Service) swiftAlternateDoc(repo *models.Repository, p swift.Path, sv string) metaDoc {
	transform := func(resp *http.Response) error {
		// A registry without that manifest redirects to Package.swift: caching
		// that under the alternate's key would serve the wrong file.
		if resp.Request == nil || resp.Request.URL.Query().Get("swift-version") != sv {
			return s.swiftRefuse(repo, "alternate manifest", errSwiftNotAlternate)
		}

		return nil
	}

	return metaDoc{
		key: swiftReleaseKey(p) + "/" + swift.ManifestName(sv),
		url: func(upstream string) (string, error) {
			u, err := upstreamURL(upstream, swiftManifestKey(p))
			if err != nil {
				return "", err
			}

			return u + "?swift-version=" + url.QueryEscape(sv), nil
		},
		opts:  staticOpts(fetchOpts{accept: swift.MediaSwift, contentType: swift.ContentTypeManifest, transform: transform}),
		limit: swiftMaxManifest, what: "swift alternate manifest", bad: badDocInternal,
	}
}

func (s *Service) swiftAlternateOf(ctx context.Context, repo *models.Repository, p swift.Path, sv string) (*resolution, []byte, error) {
	body, res, err := parseDoc(ctx, s, repo, s.swiftAlternateDoc(repo, p, sv), func(body []byte, _ *resolution) ([]byte, error) {
		return bytes.Clone(body), nil
	})

	return res, body, err
}

func (s *Service) serveSwiftManifest(w http.ResponseWriter, r *http.Request, repo *models.Repository, p swift.Path) {
	ctx := r.Context()

	g, err := s.gateFor(ctx, repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	if g.AgeActive() {
		if _, _, err = s.swiftAvailable(ctx, repo, g, p); err != nil {
			s.writeResolveError(w, r, err)
			return
		}
	}

	res, env, err := s.swiftManifestOf(ctx, repo, p)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	origin := s.publicBase(w, r)
	manifestURL := swiftURL(origin, repo, p, p.Version) + "/" + swift.ManifestFile
	content, filename := env.Content, swift.ManifestFile

	if sv := r.URL.Query().Get("swift-version"); sv != "" {
		if !slices.ContainsFunc(env.Alternates, func(a swiftAlternate) bool { return a.SwiftVersion == sv }) {
			w.Header().Set("Location", manifestURL)
			w.WriteHeader(http.StatusSeeOther)

			return
		}

		if res, content, err = s.swiftAlternateOf(ctx, repo, p, sv); err != nil {
			s.writeResolveError(w, r, err)
			return
		}

		filename = swift.ManifestName(sv)
	}

	h := w.Header()
	h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	for _, a := range env.Alternates {
		params := map[string]string{"filename": swift.ManifestName(a.SwiftVersion)}
		if a.ToolsVersion != "" {
			params["swift-tools-version"] = a.ToolsVersion
		}

		alt := swift.Link{
			URL: manifestURL + "?swift-version=" + url.QueryEscape(a.SwiftVersion), Rel: swift.RelAlternate, Params: params,
		}

		h.Add("Link", alt.String())
	}

	if res.stale {
		h.Set("Warning", staleWarning)
	}

	if base.Revalidate(w, r, repo.Public, base.ContentETag(content), swiftMaxAge) {
		return
	}

	writeBody(w, r, swift.ContentTypeManifest, content)
}

// ---- archive ----

func (s *Service) serveSwiftArchive(w http.ResponseWriter, r *http.Request, repo *models.Repository, p swift.Path) {
	ctx := r.Context()

	g, err := s.gateFor(ctx, repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	// A cached archive is served without locate: its age is checked here.
	if g.AgeActive() {
		if _, _, err = s.swiftAvailable(ctx, repo, g, p); err != nil {
			s.writeResolveError(w, r, err)
			return
		}
	}

	key := swiftArchiveKey(p)

	// The name the client asked for, not the lower-case cache key's. A refusal
	// further down is converted or has no use for it: swiftProblemWriter drops it.
	w.Header().Set("Content-Disposition", `attachment; filename="`+p.Name+"-"+p.Version+swiftArchiveExt+`"`)

	s.serveDownload(w, r, repo, key, func(ctx context.Context) (download, error) {
		_, rel, err := s.swiftAvailable(ctx, repo, g, p)
		if err != nil {
			return download{}, err
		}

		target, err := upstreamURL(repo.UpstreamURL, key)
		if err != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
			return download{}, errInternal
		}

		return download{url: target, opts: fetchOpts{
			accept: swift.MediaZip, contentType: swift.ContentTypeZip, wantSHA256: rel.doc.Checksum(),
		}}, nil
	})
}

// writeSwiftEntry is Hooks.WriteEntry: the archive's Digest header (the cache
// entry's blob key is the sha256 of its bytes). It always returns false, the
// generic writer streams the body.
func (s *Service) writeSwiftEntry(
	w http.ResponseWriter, _ *http.Request, _ *models.Repository, entry *models.ProxyCacheEntry, key string, _ io.Reader,
) bool {
	if !strings.HasSuffix(key, swiftArchiveExt) {
		return false
	}

	if digest := swift.DigestHeader(entry.BlobKey); digest != "" {
		w.Header().Set("Digest", digest)
	}

	return false
}

// ---- identifiers ----

func swiftIdentifiersDoc(rawURL string) metaDoc {
	sum := sha256.Sum256([]byte(rawURL))

	return metaDoc{
		key: swiftIdentsDir + hex.EncodeToString(sum[:]),
		url: func(upstream string) (string, error) {
			u, err := upstreamURL(upstream, "identifiers")
			if err != nil {
				return "", err
			}

			return u + "?url=" + url.QueryEscape(rawURL), nil
		},
		opts:  staticOpts(fetchOpts{accept: swift.MediaJSON, contentType: jsonMediaType, verify: verifySwiftIdentifiers}),
		limit: swift.MaxDocBytes, what: "swift identifiers", bad: badDocInternal,
	}
}

// verifySwiftIdentifiers accepts an identifiers answer: {"identifiers":["a.B"]}.
func verifySwiftIdentifiers(rs io.ReadSeeker) error {
	var doc struct {
		Identifiers []string `json:"identifiers"`
	}

	if err := json.NewDecoder(io.LimitReader(rs, swift.MaxDocBytes)).Decode(&doc); err != nil {
		return fmt.Errorf("parse identifiers: %w", err)
	}

	for _, id := range doc.Identifiers {
		if _, _, ok := swift.SplitID(id); !ok {
			return fmt.Errorf("%w %q", errSwiftIdentifier, id)
		}
	}

	return nil
}

func (s *Service) serveSwiftIdentifiers(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" || len(rawURL) > swift.MaxURLLen {
		swift.WriteProblem(w, http.StatusBadRequest, "url query parameter is missing or too long")
		return
	}

	s.serveVerbatim(w, r, repo, swiftIdentifiersDoc(rawURL))
}

// ---- hooks ----

// swiftPublishedAt is Hooks.Published: the publication time of the cached
// release document of version of pkg ("scope.name").
func (s *Service) swiftPublishedAt(ctx context.Context, repo *models.Repository, pkg, version string) (time.Time, bool) {
	scope, name, ok := swift.SplitID(pkg)
	if !ok {
		return time.Time{}, false
	}

	p := swift.Path{Kind: swift.KindRelease, Scope: scope, Name: name, Version: version}

	body, ok := s.cachedDocBody(ctx, repo, s.swiftReleaseDoc(repo, p))
	if !ok {
		return time.Time{}, false
	}

	rel, err := swiftParseRelease(p)(body, nil)
	if err != nil {
		return time.Time{}, false
	}

	published := swiftPublished(rel.doc)

	return published, !published.IsZero()
}

// swiftVersions is Hooks.Versions: the versions in the cached list of pkg.
func (s *Service) swiftVersions(ctx context.Context, repo *models.Repository, pkg string) ([]DatedVersion, bool) {
	scope, name, ok := swift.SplitID(pkg)
	if !ok {
		return nil, false
	}

	body, ok := s.cachedDocBody(ctx, repo, s.swiftListDoc(repo, swift.Path{Kind: swift.KindList, Scope: scope, Name: name}))
	if !ok {
		return nil, false
	}

	list, err := swift.ParseReleaseList(body)
	if err != nil {
		return nil, false
	}

	var out []DatedVersion
	for _, v := range list.Versions() {
		out = append(out, DatedVersion{Version: v})
	}

	return out, len(out) > 0
}
