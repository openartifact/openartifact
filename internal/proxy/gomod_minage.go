// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
func (s *Service) goInfoOf(ctx context.Context, repo *models.Repository, req goRequest, now time.Time) (goInfoDoc, error) {
	doc, _, err := parseDoc(ctx, s, repo, s.goDoc(repo, req, now), func(body []byte, _ *resolution) (goInfoDoc, error) {
		var d goInfoDoc

		// The body was checked when it was fetched; one whose Time does not
		// parse is an undated version, not an error.
		_ = json.Unmarshal(body, &d)

		return d, nil
	})

	return doc, err
}

// goPublished is the publish time of version of mod: zero for a version the
// upstream has no ".info" of (it cannot be dated).
func (s *Service) goPublished(ctx context.Context, repo *models.Repository, mod, version string, now time.Time) (time.Time, error) {
	req, ok := goInfoRequest(mod, version)
	if !ok {
		return time.Time{}, nil
	}

	doc, err := s.goInfoOf(ctx, repo, req, now)

	switch {
	case errors.Is(err, errNotFound):
		return time.Time{}, nil
	case err != nil:
		return time.Time{}, err
	}

	return doc.published(), nil
}

// goGateLocate is the locate hook of a ".mod" or ".zip" while the gate is on:
// the file is fetched from the upstream only when its version is old enough.
func (s *Service) goGateLocate(repo *models.Repository, req goRequest, now time.Time) func(context.Context) (string, error) {
	return func(fctx context.Context) (string, error) {
		published, err := s.goPublished(fctx, repo, req.module, req.version, now)
		if err != nil {
			return "", err
		}

		if tooNew(repo, published, now) {
			return "", fmt.Errorf("%s@%s: %w", req.module, req.version, errTooNew)
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
func (s *Service) serveGoGated(w http.ResponseWriter, r *http.Request, repo *models.Repository, req goRequest, now time.Time) bool {
	switch req.kind {
	case goList:
		s.serveGoList(w, r, repo, req, now)
	case goLatest:
		s.serveGoLatest(w, r, repo, req, now)
	case goInfo:
		s.serveGoInfo(w, r, repo, req, now)
	case goMod, goZip:
		return false
	}

	return true
}

// serveGoInfo answers a ".info": the upstream's, unless it dates the version
// (or the revision a query resolves to) as too new.
func (s *Service) serveGoInfo(w http.ResponseWriter, r *http.Request, repo *models.Repository, req goRequest, now time.Time) {
	doc, err := s.goInfoOf(r.Context(), repo, req, now)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	if tooNew(repo, doc.published(), now) {
		s.writeResolveError(w, r, fmt.Errorf("%s@%s: %w", req.module, doc.Version, errTooNew))
		return
	}

	s.serveVerbatim(w, r, repo, s.goDoc(repo, req, now))
}

// serveGoLatest answers "@latest": the upstream's, or the ".info" of the newest
// version that is old enough when the upstream's is too new. A module without
// such a version has no latest (404, as a module without versions).
func (s *Service) serveGoLatest(w http.ResponseWriter, r *http.Request, repo *models.Repository, req goRequest, now time.Time) {
	doc, err := s.goInfoOf(r.Context(), repo, req, now)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	if !tooNew(repo, doc.published(), now) {
		s.serveVerbatim(w, r, repo, s.goDoc(repo, req, now))
		return
	}

	escMod := strings.TrimSuffix(req.key, "/@latest")

	lines, _, err := s.goListLines(r.Context(), repo, goListRequest(req.module, escMod), now)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	walk, err := s.goGateWalk(r.Context(), repo, req.module, goListVersions(lines), now)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	info, ok := goInfoRequest(req.module, walk.newest)
	if walk.newest == "" || !ok {
		s.writeResolveError(w, r, errNotFound)
		return
	}

	s.serveVerbatim(w, r, repo, s.goDoc(repo, info, now))
}

// serveGoList answers a version list without the versions the gate hides.
func (s *Service) serveGoList(w http.ResponseWriter, r *http.Request, repo *models.Repository, req goRequest, now time.Time) {
	lines, res, err := s.goListLines(r.Context(), repo, req, now)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	walk, err := s.goGateWalk(r.Context(), repo, req.module, goListVersions(lines), now)
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
func (s *Service) goListLines(ctx context.Context, repo *models.Repository, req goRequest, now time.Time) ([]string, *resolution, error) {
	return parseDoc(ctx, s, repo, s.goDoc(repo, req, now), func(body []byte, _ *resolution) ([]string, error) {
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
	// dropped are the versions that are too new.
	dropped map[string]struct{}
	// newest is the newest version that is old enough: the highest release, or
	// the highest pre-release when no release was found. "" when none was.
	newest string
}

// goGateWalk looks at the ".info" of versions (highest first) until it finds
// the highest release that is old enough, or has made goGateProbes lookups.
func (s *Service) goGateWalk(ctx context.Context, repo *models.Repository, mod string, versions []string, now time.Time) (goWalk, error) {
	w := goWalk{dropped: map[string]struct{}{}}

	var firstEligible string

	for i, v := range versions {
		if i >= goGateProbes {
			break
		}

		published, err := s.goPublished(ctx, repo, mod, v, now)
		if err != nil {
			return goWalk{}, err
		}

		if tooNew(repo, published, now) {
			w.dropped[v] = struct{}{}
			continue
		}

		if firstEligible == "" {
			firstEligible = v
		}

		if semver.Prerelease(v) == "" {
			w.newest = v
			break
		}
	}

	if w.newest == "" {
		w.newest = firstEligible
	}

	return w, nil
}
