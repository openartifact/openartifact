// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/compactindex"
)

// The compact index (versions, names, info/<gem>) is what bundler and current
// gem read. It is generated from the listed (not yanked) gems at request time:
// the info file of a gem has one line per version (runtime dependencies and the
// SHA-256 of the .gem), the versions file one line per gem with the MD5 of that
// info file, and names lists the gem names.
//
// The files are served the way rubygems.org serves them, because bundler's
// incremental update checks them: ETag is the MD5 of the file (bundler before
// 2.5 compares it with the MD5 of what it assembled), Repr-Digest and Digest carry
// the SHA-256. Range is not honored: the files are regenerated whole, so a tail
// of one version of the versions file does not extend an older copy, and the
// client is better served with the full file than with a digest mismatch.
// Optional requirement fields (ruby, rubygems) are not generated: the stored
// metadata does not keep them.

const compactMaxAge = 60

const compactContentType = "text/plain; charset=utf-8"

func (h *HTTPHandler) handleVersions(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.compactRepo(w, r)
	if !ok {
		return
	}

	stamp, err := h.DB.RubyGemsSpecsStamp(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to stamp gem index", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	rendered, err := h.Rendered(r.Context(), "versions:"+repo.ID+":"+stamp, func(ctx context.Context) (*base.Rendered, error) {
		gems, lerr := h.DB.ListRubyGems(ctx, repo.ID)
		if lerr != nil {
			return nil, fmt.Errorf("list gems: %w", lerr)
		}

		return &base.Rendered{Body: renderVersions(indexable(gems)), ContentType: compactContentType, Opaque: true}, nil
	})
	if err != nil {
		h.Log.Errorw("failed to render gem versions", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.serveCompact(w, r, repo, rendered)
}

func (h *HTTPHandler) handleNames(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.compactRepo(w, r)
	if !ok {
		return
	}

	stamp, err := h.DB.RubyGemsSpecsStamp(r.Context(), repo.ID)
	if err != nil {
		h.Log.Errorw("failed to stamp gem index", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	rendered, err := h.Rendered(r.Context(), "names:"+repo.ID+":"+stamp, func(ctx context.Context) (*base.Rendered, error) {
		gems, lerr := h.DB.ListRubyGemSpecs(ctx, repo.ID)
		if lerr != nil {
			return nil, fmt.Errorf("list gem specs: %w", lerr)
		}

		names := make([]string, 0, len(gems))

		for _, g := range gems {
			if compactindex.ValidName(g.Name) {
				names = append(names, g.Name)
			}
		}

		return &base.Rendered{Body: compactindex.RenderNames(names), ContentType: compactContentType, Opaque: true}, nil
	})
	if err != nil {
		h.Log.Errorw("failed to render gem names", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	h.serveCompact(w, r, repo, rendered)
}

func (h *HTTPHandler) handleInfo(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.compactRepo(w, r)
	if !ok {
		return
	}

	name := chi.URLParam(r, "gem")
	if !compactindex.ValidName(name) {
		http.NotFound(w, r)
		return
	}

	gems, err := h.DB.ListRubyGemVersions(r.Context(), repo.ID, name)
	if err != nil {
		h.Log.Errorw("failed to list gem versions", "repo", repo.Name, "gem", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	gems = indexable(gems)
	if len(gems) == 0 {
		http.NotFound(w, r)
		return
	}

	h.serveCompact(w, r, repo, &base.Rendered{Body: renderInfo(gems), ContentType: compactContentType, Opaque: true})
}

// compactRepo resolves the repository of a compact index request, answering
// 404/500 itself.
func (h *HTTPHandler) compactRepo(w http.ResponseWriter, r *http.Request) (*models.Repository, bool) {
	repoName := urlparams.Vars(r)["name"]

	repo, err := h.ResolveRepo(r.Context(), repoName)
	if err != nil {
		h.Log.Errorw("failed to resolve repo", "name", repoName, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return nil, false
	}

	if repo == nil {
		http.NotFound(w, r)
		return nil, false
	}

	return repo, true
}

// serveCompact answers with the MD5 ETag, the SHA-256 digest headers and the
// whole body.
func (h *HTTPHandler) serveCompact(w http.ResponseWriter, r *http.Request, repo *models.Repository, rendered *base.Rendered) {
	etag := `"` + compactindex.InfoMD5(rendered.Body) + `"`
	if base.Revalidate(w, r, repo.Public, etag, compactMaxAge) {
		return
	}

	sum := sha256.Sum256(rendered.Body)
	digest := compactindex.DigestValue(sum[:])
	w.Header().Set("Digest", digest)
	w.Header().Set("Repr-Digest", digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(rendered.Body)))

	rendered.Serve(w, r)
}

// indexable keeps the listed gems the compact index can carry: names, version
// fields and checksums the line format represents (a gem pushed with an unusual
// platform spelling is served by the legacy indexes only).
func indexable(gems []*models.RubyGem) []*models.RubyGem {
	return slices.DeleteFunc(liveGems(gems), func(g *models.RubyGem) bool {
		return !compactindex.ValidName(g.Name) || !compactindex.ValidVersionField(gemVersionField(g)) ||
			!compactindex.ValidSHA256(g.ChecksumSHA256)
	})
}

// renderVersions renders the versions file of live: a line per gem with its
// versions oldest first and the MD5 of its info file. created_at is the first
// publication, so equal data gives equal bytes.
func renderVersions(live []*models.RubyGem) []byte {
	byName := make(map[string][]*models.RubyGem)

	var created time.Time

	for _, g := range live {
		byName[g.Name] = append(byName[g.Name], g)

		if !g.CreatedAt.IsZero() && (created.IsZero() || g.CreatedAt.Before(created)) {
			created = g.CreatedAt
		}
	}

	v := &compactindex.Versions{}
	if !created.IsZero() {
		v.CreatedAt = created.UTC().Format(time.RFC3339)
	}

	for name, gems := range byName {
		sortGemsAscending(gems)

		versions := make([]string, len(gems))
		for i, g := range gems {
			versions[i] = gemVersionField(g)
		}

		v.Entries = append(v.Entries, compactindex.Entry{Name: name, Versions: versions, InfoMD5: compactindex.InfoMD5(renderInfo(gems))})
	}

	return v.Render()
}

// renderInfo renders the info file of the live versions of one gem.
func renderInfo(gems []*models.RubyGem) []byte {
	gems = slices.Clone(gems)
	sortGemsAscending(gems)

	lines := make([]compactindex.InfoLine, len(gems))
	for i, g := range gems {
		lines[i] = compactindex.InfoLine{Raw: infoLine(g), Version: gemVersionField(g), Checksum: g.ChecksumSHA256}
	}

	return compactindex.RenderInfo(lines)
}

// sortGemsAscending orders versions oldest first; within a version the ruby
// platform comes first, then the others alphabetically.
func sortGemsAscending(gems []*models.RubyGem) {
	platform := func(g *models.RubyGem) string {
		if g.Platform == "ruby" {
			return ""
		}

		return g.Platform
	}

	slices.SortStableFunc(gems, func(a, b *models.RubyGem) int {
		return cmp.Or(compareVersions(a.Version, b.Version), strings.Compare(platform(a), platform(b)))
	})
}

// gemVersionField is the first field of an info line, and the name of the gem's
// file without "<name>-": the version, then "-<platform>" unless it is "ruby".
func gemVersionField(g *models.RubyGem) string {
	if g.Platform == "" || g.Platform == "ruby" {
		return g.Version
	}

	return g.Version + "-" + g.Platform
}

// infoLine renders one version: its runtime dependencies as name:req&req, then
// "|checksum:<sha256>".
func infoLine(g *models.RubyGem) string {
	var deps []gemDependencyJSON

	_ = json.Unmarshal(g.Dependencies, &deps) // an unreadable column lists no dependencies

	parts := make([]string, 0, len(deps))

	for _, d := range deps {
		if d.Type == "development" || !compactindex.ValidName(d.Name) {
			continue
		}

		if req, ok := compactRequirement(d.Requirement); ok {
			parts = append(parts, d.Name+":"+req)
		}
	}

	slices.Sort(parts)

	return gemVersionField(g) + " " + strings.Join(slices.Compact(parts), ",") + "|checksum:" + g.ChecksumSHA256
}

// compactRequirement converts a requirement as the gemspec stores it
// (">= 1.0, < 3") to the compact index's (">= 1.0&< 3"). A clause the line format
// cannot carry (a separator or control character) makes ok false.
func compactRequirement(req string) (string, bool) {
	var clauses []string

	for c := range strings.SplitSeq(req, ",") {
		if c = strings.TrimSpace(c); c != "" {
			clauses = append(clauses, c)
		}
	}

	if len(clauses) == 0 {
		return ">= 0", true
	}

	for _, c := range clauses {
		if strings.ContainsAny(c, "&|\r\n\x00") {
			return "", false
		}
	}

	return strings.Join(clauses, "&"), true
}
