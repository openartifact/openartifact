// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/database/models"
)

// MaxGlobLength bounds a policy glob.
const MaxGlobLength = 256

// ErrInvalidGlob is returned for a glob that is too long or does not compile.
var ErrInvalidGlob = errors.New("invalid glob")

// CompileGlob translates a policy glob into an anchored regular expression:
// `*` matches any run of characters except '/', `**` matches anything
// (including '/'; a `**/` prefix also matches the empty path prefix), `?`
// matches one character except '/'. Everything else is literal; newlines count
// as ordinary characters. The result is an RE2 expression, so matching is
// linear in the input.
func CompileGlob(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > MaxGlobLength {
		return nil, fmt.Errorf("%w: longer than %d characters", ErrInvalidGlob, MaxGlobLength)
	}

	var b strings.Builder

	b.WriteString(`(?s)^`) // (?s): `.` also matches a newline, so `**` spans any path

	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '*' && strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString(`(?:.*/)?`)

			i += 2
		case c == '*' && strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(`.*`)

			i++
		case c == '*':
			b.WriteString(`[^/]*`)
		case c == '?':
			b.WriteString(`[^/]`)
		default:
			// Decode whole runes: quoting single bytes would corrupt multi-byte UTF-8.
			r, n := utf8.DecodeRuneInString(pattern[i:])
			b.WriteString(regexp.QuoteMeta(string(r)))

			i += n - 1
		}
	}

	b.WriteString(`$`)

	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidGlob, err)
	}

	return re, nil
}

// Policy is a compiled lifecycle policy.
type Policy struct {
	ID           string
	Name         string
	Enabled      bool
	Position     int
	TargetTierID string
	FromTierID   string // "" = any tier
	MinAge       *time.Duration
	MinIdle      *time.Duration
	MinSize      *int64
	MaxSize      *int64
	RepoGlob     string
	Formats      []string
	PathGlob     string

	repo *regexp.Regexp
	path *regexp.Regexp
}

// CompilePolicy validates and compiles a stored policy.
func CompilePolicy(m *models.StoragePolicy) (*Policy, error) {
	p := &Policy{
		ID: m.ID, Name: m.Name, Enabled: m.Enabled, Position: m.Position,
		TargetTierID: m.TargetTierID, FromTierID: m.FromTierID,
		MinSize: m.MinSizeBytes, MaxSize: m.MaxSizeBytes,
		RepoGlob: m.RepoGlob, Formats: slices.Clone(m.Formats), PathGlob: m.PathGlob,
	}

	if m.MinAgeSeconds != nil {
		d := secondsDuration(*m.MinAgeSeconds)
		p.MinAge = &d
	}

	if m.MinIdleSeconds != nil {
		d := secondsDuration(*m.MinIdleSeconds)
		p.MinIdle = &d
	}

	var err error

	if m.RepoGlob != "" {
		if p.repo, err = CompileGlob(m.RepoGlob); err != nil {
			return nil, fmt.Errorf("repo_glob: %w", err)
		}
	}

	if m.PathGlob != "" {
		if p.path, err = CompileGlob(m.PathGlob); err != nil {
			return nil, fmt.Errorf("path_glob: %w", err)
		}
	}

	return p, nil
}

// secondsDuration converts seconds to a duration, saturating instead of overflowing.
func secondsDuration(s int64) time.Duration {
	const maxSeconds = int64(1<<63-1) / int64(time.Second)
	if s > maxSeconds {
		return time.Duration(1<<63 - 1)
	}

	return time.Duration(s) * time.Second
}

// NeedsRefs reports whether matching reads the blob's repository references.
func (p *Policy) NeedsRefs() bool {
	return p.repo != nil || p.path != nil || len(p.Formats) > 0
}

// Facts is everything a policy is evaluated against for one blob.
type Facts struct {
	SHA256         string
	SizeBytes      int64
	CreatedAt      time.Time
	LastAccessedAt *time.Time
	// TierID is the tier the blob is placed on.
	TierID string
	// Refs are the repository entries referencing the blob; only needed (and
	// loaded) when a policy NeedsRefs.
	Refs []models.BlobRef
}

// lastTouch is the time idleness is measured from: the last download, else the
// time the blob was first stored.
func (f *Facts) lastTouch() time.Time {
	if f.LastAccessedAt != nil && f.LastAccessedAt.After(f.CreatedAt) {
		return *f.LastAccessedAt
	}

	return f.CreatedAt
}

// Matches reports whether the policy's conditions hold for the blob at now. It
// says nothing about whether the blob already is on the policy's target tier.
func (p *Policy) Matches(f *Facts, now time.Time) bool {
	if !p.Enabled {
		return false
	}

	if p.FromTierID != "" && f.TierID != p.FromTierID {
		return false
	}

	if p.MinAge != nil && now.Sub(f.CreatedAt) < *p.MinAge {
		return false
	}

	if p.MinIdle != nil && now.Sub(f.lastTouch()) < *p.MinIdle {
		return false
	}

	if p.MinSize != nil && f.SizeBytes < *p.MinSize {
		return false
	}

	if p.MaxSize != nil && f.SizeBytes > *p.MaxSize {
		return false
	}

	if !p.NeedsRefs() {
		return true
	}

	return slices.ContainsFunc(f.Refs, p.matchesRef)
}

// matchesRef reports whether one repository entry satisfies every set
// repository, format and path condition.
func (p *Policy) matchesRef(r models.BlobRef) bool {
	if p.repo != nil && !p.repo.MatchString(r.Repo) {
		return false
	}

	if len(p.Formats) > 0 && !slices.Contains(p.Formats, r.Format) {
		return false
	}

	return p.path == nil || p.path.MatchString(r.Path)
}

// Decide returns the first policy (in the given order) that matches the blob,
// or nil. The caller sorts policies by position.
func Decide(policies []*Policy, f *Facts, now time.Time) *Policy {
	for _, p := range policies {
		if p.Matches(f, now) {
			return p
		}
	}

	return nil
}

// SortPolicies orders policies by position, then name (the storage order).
func SortPolicies(policies []*Policy) {
	slices.SortStableFunc(policies, func(a, b *Policy) int {
		if a.Position != b.Position {
			return a.Position - b.Position
		}

		return strings.Compare(a.Name, b.Name)
	})
}

// ScanClause expresses the policy's SQL-checkable conditions as a candidate
// filter. backendOf maps a tier id to its backend id ("" = primary store). The
// clause only has to be a necessary condition of Matches (plus "not already on
// the target"): the exact decision is Decide on the returned page.
func (p *Policy) ScanClause(now time.Time, backendOf func(tierID string) string) models.TierScanClause {
	c := models.TierScanClause{
		TargetBackendID: backendOf(p.TargetTierID),
		MinSizeBytes:    p.MinSize,
		MaxSizeBytes:    p.MaxSize,
	}

	if p.FromTierID != "" {
		c.HasFrom = true
		c.FromBackendID = backendOf(p.FromTierID)
	}

	if p.MinAge != nil {
		t := now.Add(-*p.MinAge)
		c.CreatedBefore = &t
	}

	if p.MinIdle != nil {
		t := now.Add(-*p.MinIdle)
		c.IdleBefore = &t
	}

	return c
}
