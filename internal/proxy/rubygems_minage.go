// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/pkg/compactindex"
)

// The minimum-age gate of the RubyGems proxy (see minage.go).
//
// The compact index states no times, so the publish time of a version is read
// from the gem server's API, /api/v1/versions/<gem>.json ("created_at" of each
// version; rubygems.org serves it), fetched per gem while the gate is on and
// cached like any other metadata document. With those dates:
//
//   - info/<gem> lists only the versions old enough. A version the API does not
//     list is too new (the API is read from the cache and may be older than the
//     info file); a gem left without versions answers 404.
//   - a .gem file, and a gemspec fetched for one, of a version that is too
//     new answers 403 when it is not cached yet.
//
// The gate cannot be applied where the API is not served: an upstream that
// answers 404 for it cannot date its versions, which then pass (as every
// version of unknown age does). The other documents state nothing that can be
// filtered reliably and are served as the upstream sends them: versions
// (whose lines carry the MD5 of the info file of the gem, a file the gate
// serves narrowed, so a client re-reads such a gem's info on every run),
// names and the legacy Marshal indexes.

const (
	// maxRubyGemsAPIBytes bounds one gem's versions API document read into memory.
	maxRubyGemsAPIBytes = 64 << 20

	// rubygemsAPIDir is the key prefix of the gem server's API documents,
	// which only the gate reads.
	rubygemsAPIDir = "api/v1/versions/"
)

var errNotGemVersions = errors.New("upstream answer is not a gem versions document")

// rubygemsAPIDoc is the versions API document of a gem.
func rubygemsAPIDoc(gem string) metaDoc {
	return metaDoc{
		key:   rubygemsAPIDir + gem + ".json",
		opts:  staticOpts(jsonDocOpts(verifyGemVersions)),
		limit: maxRubyGemsAPIBytes,
		what:  "gem versions",
		bad:   badDocUpstream,
	}
}

// verifyGemVersions refuses an upstream answer that is not a JSON array (an
// HTML page served with 200, a truncated document).
func verifyGemVersions(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	body, err := io.ReadAll(io.LimitReader(spool, maxRubyGemsAPIBytes+1))
	if err != nil {
		return fmt.Errorf("read spool: %w", err)
	}

	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(body) > maxRubyGemsAPIBytes || !json.Valid(body) || len(trimmed) == 0 || trimmed[0] != '[' {
		return errNotGemVersions
	}

	return nil
}

// parseGemDates reads the versions API document: when each "<number>[-<platform>]"
// (the info file's version field) was created. nil for a document without
// versions. A version whose time is unreadable maps to the zero time.
func parseGemDates(doc []byte) (map[string]time.Time, error) {
	var in []struct {
		Number    string `json:"number"`
		Platform  string `json:"platform"`
		CreatedAt string `json:"created_at"`
	}

	if err := json.Unmarshal(doc, &in); err != nil {
		return nil, fmt.Errorf("parse gem versions: %w", err)
	}

	if len(in) == 0 {
		return nil, nil //nolint:nilnil // no versions: nothing to date
	}

	dates := make(map[string]time.Time, len(in))

	for _, v := range in {
		key := v.Number
		if v.Platform != "" && v.Platform != "ruby" {
			key += "-" + v.Platform
		}

		created, _ := time.Parse(time.RFC3339Nano, v.CreatedAt)

		// A version stored once per ABI keeps its newest time.
		if prev, dup := dates[key]; !dup || created.After(prev) {
			dates[key] = created
		}
	}

	return dates, nil
}

// gemNumber is the version of an info file's version field, which carries a
// "-<platform>" suffix for a platform gem (a version never holds a hyphen).
// Curation rules name the version, so every platform of it is covered.
func gemNumber(version string) string {
	number, _, _ := strings.Cut(version, "-")

	return number
}

// gemPublished is the time the gate is asked about for the version field of an
// info line: the API's created_at, and now for a version the API does not list
// (it is too new: the API is read from the cache and may be older than the info
// file). dates is nil when the minimum age is off or the upstream cannot date
// its versions: the time is then unknown (zero).
func gemPublished(dates map[string]time.Time, version string, now time.Time) time.Time {
	if dates == nil {
		return time.Time{}
	}

	if created, ok := dates[version]; ok {
		return created
	}

	return now
}

// gateRubyGemsInfo returns the info lines of gem that the gate leaves available.
func gateRubyGemsInfo(g versionGate, gem string, lines []compactindex.InfoLine, dates map[string]time.Time) []compactindex.InfoLine {
	kept := make([]compactindex.InfoLine, 0, len(lines))

	for _, l := range lines {
		if hidden, _ := g.Hidden(gem, gemNumber(l.Version), gemPublished(dates, l.Version, g.Now)); !hidden {
			kept = append(kept, l)
		}
	}

	return kept
}

// rubygemsDates reads the dates of the versions of gem through the cache. A nil
// map (and no error) means the upstream cannot date them: the API is not served
// or lists no version.
func (s *Service) rubygemsDates(ctx context.Context, repo *models.Repository, gem string) (map[string]time.Time, *resolution, error) {
	dates, res, err := parseDoc(ctx, s, repo, rubygemsAPIDoc(gem), func(body []byte, _ *resolution) (map[string]time.Time, error) {
		return parseGemDates(body)
	})
	if errors.Is(err, errNotFound) {
		return nil, nil, nil
	}

	return dates, res, err
}

// checkRubyGemsFile refuses the file of version sp of a gem that the gate holds
// back: curation, and a version published too recently (the dates are only
// looked up when the minimum age is on); strict makes a version the API does
// not list too new.
func (s *Service) checkRubyGemsFile(
	ctx context.Context,
	repo *models.Repository,
	g versionGate,
	sp compactindex.FileSplit,
	strict bool,
) error {
	var published time.Time

	if g.AgeActive() {
		dates, _, err := s.rubygemsDates(ctx, repo, sp.Name)
		if err != nil {
			return err
		}

		if dates != nil {
			var listed bool

			if published, listed = dates[sp.Version]; !listed && strict {
				published = g.Now
			}
		}
	}

	return checkGate(g, sp.Name, gemNumber(sp.Version), published)
}

// rubygemsReadings returns the readings of a gem file name (see
// compactindex.SplitFile) the gate must judge. With curation on, the one
// reading whose cached info file lists the version, when there is one: the other
// readings name gems that are not the file's, and a rule on one of those must not
// refuse it. Otherwise every reading.
func (s *Service) rubygemsReadings(
	ctx context.Context,
	repo *models.Repository,
	g versionGate,
	file string,
) ([]compactindex.FileSplit, error) {
	splits := compactindex.SplitFile(file)
	if !g.Set.Active() {
		return splits, nil
	}

	for _, sp := range splits {
		line, err := s.rubygemsInfoLine(ctx, repo, sp.Name, sp.Version)

		switch {
		case errors.Is(err, errNotFound):
			continue
		case err != nil:
			return nil, err
		case line != nil:
			return []compactindex.FileSplit{sp}, nil
		}
	}

	return splits, nil
}

// checkRubyGemspec refuses the gemspec of a version the gate holds back (see
// checkRubyGemsFile). The file name does not say where the name ends: a reading
// whose gem does not list the version is another reading.
func (s *Service) checkRubyGemspec(ctx context.Context, repo *models.Repository, g versionGate, file string) error {
	splits, err := s.rubygemsReadings(ctx, repo, g, file)
	if err != nil {
		return err
	}

	for _, sp := range splits {
		if err := s.checkRubyGemsFile(ctx, repo, g, sp, false); err != nil {
			return err
		}
	}

	return nil
}

// checkRubyGemCurated refuses a gem or gemspec file of a version curation makes
// unavailable, without any publish time (nothing is fetched for one). It is what
// guards a file already in the cache, which no fetch step looks at.
func (s *Service) checkRubyGemCurated(ctx context.Context, repo *models.Repository, g versionGate, file string) error {
	if !g.Set.Active() {
		return nil
	}

	g.MinAge = 0

	return s.checkRubyGemspec(ctx, repo, g, file)
}

// rubygemsPublished is Hooks.Published: the created_at of version of gem in the
// cached versions API document, the newest of its platforms.
func (s *Service) rubygemsPublished(ctx context.Context, repo *models.Repository, gem, version string) (time.Time, bool) {
	body, ok := s.cachedDocBody(ctx, repo, rubygemsAPIDoc(gem))
	if !ok {
		return time.Time{}, false
	}

	dates, err := parseGemDates(body)
	if err != nil {
		return time.Time{}, false
	}

	var newest time.Time

	for field, created := range dates {
		if gemNumber(field) == version && created.After(newest) {
			newest = created
		}
	}

	return newest, !newest.IsZero()
}

// serveRubyGemsInfo answers info/<gem> of a repository with the gate on: the
// cached info file without the versions the gate holds back. Without curation a
// gem the upstream cannot date is served as it is; curation needs no dates (they
// are looked up only when the minimum age is on).
func (s *Service) serveRubyGemsInfo(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	g versionGate,
	p compactindex.Path,
	key string,
) {
	d := rubygemsInfoDoc(p.Gem)

	var (
		dates    map[string]time.Time
		datesRes *resolution
	)

	// The dates come first: they take a document slot of their own.
	if g.AgeActive() {
		var err error

		if dates, datesRes, err = s.rubygemsDates(r.Context(), repo, p.Gem); err != nil {
			s.writeResolveError(w, r, err)
			return
		}
	}

	if dates == nil && !g.Set.Active() {
		s.serveVerbatim(w, r, repo, d)
		return
	}

	res, err := s.resolveDoc(r.Context(), repo, d)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	loaded, body, release, err := s.loadDoc(r.Context(), repo, d, res)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	lines, perr := compactindex.ParseInfoBytes(body)
	if perr != nil {
		release()
		s.writeResolveError(w, r, s.unusableDoc(r.Context(), repo, d, perr))

		return
	}

	kept := gateRubyGemsInfo(g, p.Gem, lines, dates)

	var out []byte
	if len(kept) > 0 && len(kept) < len(lines) {
		out = compactindex.RenderInfo(kept)
	}

	release()

	switch {
	case len(kept) == len(lines):
		// Nothing hidden: the upstream's file, served the way it is cached.
		s.serveResolved(w, r, repo, key, loaded, func() (*resolution, error) { return s.resolveDoc(r.Context(), repo, d) })

		return
	case len(kept) == 0:
		http.NotFound(w, r)

		return
	}

	if quarantine.Enforce(w, r, s.db, repo.ID, quarantine.PathKeys(key, loaded.entry.ID)...) {
		return
	}

	if loaded.stale || (datesRes != nil && datesRes.stale) {
		w.Header().Set("Warning", staleWarning)
	}

	sha := sha256.Sum256(out)
	digest := `sha-256="` + base64.StdEncoding.EncodeToString(sha[:]) + `"`
	s.writeCompactFile(w, r, repo, compactindex.InfoMD5(out), digest, int64(len(out)), bytes.NewReader(out))
}
