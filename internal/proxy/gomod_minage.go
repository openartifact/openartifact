// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
)

// The minimum-age gate of a Go proxy (see minage.go).
//
// The GOPROXY protocol dates a version only in the "Time" of its ".info"
// document (the time of the revision the version names); "list" has no times
// and "@latest" is the ".info" of the newest version. So the gate is built on
// the ".info" documents, which are small and immutable once cached:
//
//   - ".info" of a too-new version (or query) answers errTooNew; ".mod" and
//     ".zip" are refused the same way when they are fetched, after one extra
//     ".info" lookup of their version.
//   - "@latest" is served as it is unless its version is too new; then it is
//     the ".info" of the newest eligible version of the module's list.
//   - "list" leaves out the too-new versions it finds by looking at the ".info"
//     of the newest versions, highest first, up to the first release that is
//     old enough: everything below it is taken to be old enough as well. That
//     bounds the lookups (one for a module whose latest release is old) while
//     keeping "go get module@latest" and "@v1" style queries from picking a
//     version the ".info" would then refuse. A version older in the list but
//     published recently (a backport) is not hidden from the list, only
//     refused by its ".info", ".mod" and ".zip".
//
// An upstream that cannot date a version (no ".info", no Time) passes it.

const (
	// goGateProbes bounds the ".info" lookups one list or @latest request makes.
	goGateProbes = 32

	// goGatedMaxAge is how long shared caches may reuse a gated list.
	goGatedMaxAge = 60
)

// goInfoRequest is the request of the ".info" of version of mod.
func goInfoRequest(mod, version string) (goRequest, bool) {
	escMod, err := module.EscapePath(mod)
	if err != nil {
		return goRequest{}, false
	}

	escVersion, err := module.EscapeVersion(version)
	if err != nil {
		return goRequest{}, false
	}

	return goRequest{kind: goInfo, module: mod, version: version, key: escMod + "/@v/" + escVersion + ".info"}, true
}

// goListRequest is the request of the version list of mod, whose escaped path
// is escMod.
func goListRequest(mod, escMod string) goRequest {
	return goRequest{kind: goList, module: mod, key: escMod + "/@v/list"}
}

// goInfoDoc is what the gate reads of a ".info" or "@latest" body.
type goInfoDoc struct {
	Version string
	Time    string
}

// published is the Time of the document, the zero time when it has none or an
// unreadable one.
func (d goInfoDoc) published() time.Time {
	t, err := time.Parse(time.RFC3339, d.Time)
	if err != nil {
		return time.Time{}
	}

	return t
}

// goInfoOf reads the ".info" or "@latest" document req names.
func (s *Service) goInfoOf(ctx context.Context, repo *models.Repository, req goRequest, g versionGate) (goInfoDoc, error) {
	doc, _, err := parseDoc(ctx, s, repo, s.goDoc(repo, req, g), func(body []byte, _ *resolution) (goInfoDoc, error) {
		var d goInfoDoc

		// The body was checked when it was fetched; one whose Time does not
		// parse is an undated version, not an error.
		_ = json.Unmarshal(body, &d)

		return d, nil
	})

	return doc, err
}

// goPublished is the publish time of version of mod: zero for a version the
// upstream has no ".info" of (it cannot be dated), and without a lookup when the
// gate has no minimum age (curation needs no dates).
func (s *Service) goPublished(ctx context.Context, repo *models.Repository, g versionGate, mod, version string) (time.Time, error) {
	if !g.AgeActive() {
		return time.Time{}, nil
	}

	req, ok := goInfoRequest(mod, version)
	if !ok {
		return time.Time{}, nil
	}

	doc, err := s.goInfoOf(ctx, repo, req, g)

	switch {
	case errors.Is(err, errNotFound):
		return time.Time{}, nil
	case err != nil:
		return time.Time{}, err
	}

	return doc.published(), nil
}

// goGateLocate is the locate hook of a ".mod" or ".zip" while the gate is on:
// the file is fetched from the upstream only when the gate leaves its version
// available (the minimum age costs one ".info" lookup, curation none).
func (s *Service) goGateLocate(repo *models.Repository, req goRequest, g versionGate) func(context.Context) (string, error) {
	return func(fctx context.Context) (string, error) {
		published, err := s.goPublished(fctx, repo, g, req.module, req.version)
		if err != nil {
			return "", err
		}

		if err := checkGate(g, req.module, req.version, published); err != nil {
			return "", err
		}

		target, err := goUpstreamURL(repo.UpstreamURL, req.key)
		if err != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
			return "", errInternal
		}

		return target, nil
	}
}

// serveGoGated answers a list, @latest or .info request while the gate is on.
// It reports false for a request the gate does not touch.
func (s *Service) serveGoGated(w http.ResponseWriter, r *http.Request, repo *models.Repository, req goRequest, g versionGate) bool {
	switch req.kind {
	case goList:
		s.serveGoList(w, r, repo, req, g)
	case goLatest:
		s.serveGoLatest(w, r, repo, req, g)
	case goInfo:
		s.serveGoInfo(w, r, repo, req, g)
	case goMod, goZip:
		return false
	}

	return true
}

// serveGoInfo answers a ".info": the upstream's, unless the gate holds back the
// version (or the revision a query resolves to).
func (s *Service) serveGoInfo(w http.ResponseWriter, r *http.Request, repo *models.Repository, req goRequest, g versionGate) {
	doc, err := s.goInfoOf(r.Context(), repo, req, g)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	if err := checkGate(g, req.module, doc.Version, doc.published()); err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveVerbatim(w, r, repo, s.goDoc(repo, req, g))
}

// serveGoLatest answers "@latest": the upstream's, or the ".info" of the newest
// version the gate leaves available when the upstream's is held back. A module
// without such a version has no latest (404, as a module without versions).
func (s *Service) serveGoLatest(w http.ResponseWriter, r *http.Request, repo *models.Repository, req goRequest, g versionGate) {
	doc, err := s.goInfoOf(r.Context(), repo, req, g)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	if hidden, _ := g.Hidden(req.module, doc.Version, doc.published()); !hidden {
		s.serveVerbatim(w, r, repo, s.goDoc(repo, req, g))
		return
	}

	escMod := strings.TrimSuffix(req.key, "/@latest")

	lines, _, err := s.goListLines(r.Context(), repo, goListRequest(req.module, escMod), g)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	walk, err := s.goGateWalk(r.Context(), repo, g, req.module, goListVersions(lines))
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	info, ok := goInfoRequest(req.module, walk.newest)
	if walk.newest == "" || !ok {
		s.writeResolveError(w, r, errNotFound)
		return
	}

	s.serveVerbatim(w, r, repo, s.goDoc(repo, info, g))
}

// serveGoList answers a version list without the versions the gate hides.
func (s *Service) serveGoList(w http.ResponseWriter, r *http.Request, repo *models.Repository, req goRequest, g versionGate) {
	lines, res, err := s.goListLines(r.Context(), repo, req, g)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	walk, err := s.goGateWalk(r.Context(), repo, g, req.module, goListVersions(lines))
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	var out strings.Builder

	for _, line := range lines {
		if f := strings.Fields(line); len(f) > 0 {
			if _, hidden := walk.dropped[f[0]]; hidden {
				continue
			}
		}

		out.WriteString(line)
		out.WriteByte('\n')
	}

	// The validator is the digest of what is sent, so it changes when a
	// version ages past the threshold and the cached list stays the same.
	body := []byte(out.String())

	if res.stale {
		w.Header().Set("Warning", staleWarning)
	}

	if base.Revalidate(w, r, repo.Public, base.ContentETag(body), goGatedMaxAge) {
		return
	}

	writeBody(w, r, "text/plain; charset=utf-8", body)
}

// goListLines reads the non-blank lines of the cached version list req.
func (s *Service) goListLines(ctx context.Context, repo *models.Repository, req goRequest, g versionGate) ([]string, *resolution, error) {
	return parseDoc(ctx, s, repo, s.goDoc(repo, req, g), func(body []byte, _ *resolution) ([]string, error) {
		var lines []string

		for line := range strings.SplitSeq(string(body), "\n") {
			if strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}

		return lines, nil
	})
}

// goListVersions is the distinct versions of list lines, highest first.
func goListVersions(lines []string) []string {
	versions := make([]string, 0, len(lines))

	for _, line := range lines {
		if f := strings.Fields(line); len(f) > 0 && semver.IsValid(f[0]) {
			versions = append(versions, f[0])
		}
	}

	slices.SortStableFunc(versions, func(a, b string) int { return semver.Compare(b, a) })

	return slices.Compact(versions)
}

// goWalk is what looking at the newest versions of a module found.
type goWalk struct {
	// dropped are the versions the gate holds back.
	dropped map[string]struct{}
	// newest is the newest version the gate leaves available: the highest
	// release, or the highest pre-release when no release was found. "" when
	// none was.
	newest string
}

// goGateWalk applies the gate to versions (highest first). Curation rules need
// no lookup and judge every version. The minimum age needs the ".info" of a
// version: they are looked at until the highest release that is old enough is
// found (everything below it is taken to be old enough), or goGateProbes
// lookups were made; the newest version is selected among those looked at.
func (s *Service) goGateWalk(ctx context.Context, repo *models.Repository, g versionGate, mod string, versions []string) (goWalk, error) {
	w := goWalk{dropped: map[string]struct{}{}}

	var (
		firstEligible string
		selecting     = true
		probes        int
	)

	for _, v := range versions {
		if hidden, _ := g.Hidden(mod, v, time.Time{}); hidden {
			w.dropped[v] = struct{}{}
			continue
		}

		if !selecting {
			continue
		}

		if g.AgeActive() {
			if probes >= goGateProbes {
				selecting = false
				continue
			}

			probes++

			published, err := s.goPublished(ctx, repo, g, mod, v)
			if err != nil {
				return goWalk{}, err
			}

			if hidden, _ := g.Hidden(mod, v, published); hidden {
				w.dropped[v] = struct{}{}
				continue
			}
		}

		if firstEligible == "" {
			firstEligible = v
		}

		if semver.Prerelease(v) == "" {
			w.newest = v
			selecting = false
		}
	}

	if w.newest == "" {
		w.newest = firstEligible
	}

	return w, nil
}

// goInfoPublished is Hooks.Published: the Time of the cached ".info" of version
// (with its "v") of mod.
func (s *Service) goInfoPublished(ctx context.Context, repo *models.Repository, mod, version string) (time.Time, bool) {
	req, ok := goInfoRequest(mod, version)
	if !ok {
		return time.Time{}, false
	}

	body, ok := s.cachedDocBody(ctx, repo, s.goDoc(repo, req, versionGate{}))
	if !ok {
		return time.Time{}, false
	}

	var doc goInfoDoc
	if json.Unmarshal(body, &doc) != nil {
		return time.Time{}, false
	}

	published := doc.published()

	return published, !published.IsZero()
}
