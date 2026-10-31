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

// gemTooNew reports whether the version field of an info line is too new at
// now by dates: not listed, or created less than the minimum age ago.
func gemTooNew(repo *models.Repository, dates map[string]time.Time, version string, now time.Time) bool {
	created, ok := dates[version]

	return !ok || tooNew(repo, created, now)
}

// gateRubyGemsInfo returns the info lines of versions old enough at now.
func gateRubyGemsInfo(
	repo *models.Repository,
	lines []compactindex.InfoLine,
	dates map[string]time.Time,
	now time.Time,
) []compactindex.InfoLine {
	kept := make([]compactindex.InfoLine, 0, len(lines))

	for _, l := range lines {
		if !gemTooNew(repo, dates, l.Version, now) {
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

// checkRubyGemsFile refuses the file of version sp of a gem published too
// recently; strict makes a version the API does not list too new.
func (s *Service) checkRubyGemsFile(
	ctx context.Context,
	repo *models.Repository,
	sp compactindex.FileSplit,
	strict bool,
	now time.Time,
) error {
	dates, _, err := s.rubygemsDates(ctx, repo, sp.Name)
	if err != nil || dates == nil {
		return err
	}

	created, listed := dates[sp.Version]
	if (!listed && strict) || tooNew(repo, created, now) {
		return fmt.Errorf("gem %s %s: %w", sp.Name, sp.Version, errTooNew)
	}

	return nil
}

// checkRubyGemspec refuses the gemspec of a version published too recently
// (see checkRubyGemsFile). The file name does not say where the name ends: a
// reading whose gem does not list the version is another reading.
func (s *Service) checkRubyGemspec(ctx context.Context, repo *models.Repository, file string, now time.Time) error {
	for _, sp := range compactindex.SplitFile(file) {
		if err := s.checkRubyGemsFile(ctx, repo, sp, false, now); err != nil {
			return err
		}
	}

	return nil
}

// serveRubyGemsInfo answers info/<gem> of a repository with the gate on: the
// cached info file without the versions that are too new. A gem the upstream
// cannot date is served as it is.
func (s *Service) serveRubyGemsInfo(w http.ResponseWriter, r *http.Request, repo *models.Repository, p compactindex.Path, key string) {
	now := time.Now()
	d := rubygemsInfoDoc(p.Gem)

	// The dates come first: they take a document slot of their own.
	dates, datesRes, err := s.rubygemsDates(r.Context(), repo, p.Gem)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	if dates == nil {
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

	kept := gateRubyGemsInfo(repo, lines, dates, now)

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

	if loaded.stale || datesRes.stale {
		w.Header().Set("Warning", staleWarning)
	}

	sha := sha256.Sum256(out)
	digest := `sha-256="` + base64.StdEncoding.EncodeToString(sha[:]) + `"`
	s.writeCompactFile(w, r, repo, compactindex.InfoMD5(out), digest, int64(len(out)), bytes.NewReader(out))
}
