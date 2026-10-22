// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestCompileGlob(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pattern, input string
		want           bool
	}{
		{"releases", "releases", true},
		{"releases", "releases-2", false},
		{"rel*", "releases", true},
		{"rel*", "rel/eases", false},
		{"*", "a/b", false},
		{"**", "a/b/c", true},
		{"a/**", "a/b/c.jar", true},
		{"a/**", "b/a/c.jar", false},
		{"**/c.jar", "c.jar", true},
		{"**/c.jar", "a/b/c.jar", true},
		{"**/c.jar", "a/b/xc.jar", false},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/b/b/c", false},
		{"v?.0", "v1.0", true},
		{"v?.0", "v10.0", false},
		{"v?.0", "v/.0", false},
		{"a.b+c(d)", "a.b+c(d)", true},
		{"a.b", "axb", false},
		{"**.jar", "a/b/c.jar", true},
		{"caf\u00e9/*", "caf\u00e9/x", true},
		{"caf\u00e9/*", "cafe/x", false},
		{"**", "a\nb", true},
		{"a/**", "a/b\nc/d", true},
		{"**/c.jar", "x\ny/c.jar", true},
	} {
		re, err := CompileGlob(tc.pattern)
		if err != nil {
			t.Fatalf("CompileGlob(%q): %v", tc.pattern, err)
		}

		if got := re.MatchString(tc.input); got != tc.want {
			t.Errorf("glob %q on %q = %v, want %v", tc.pattern, tc.input, got, tc.want)
		}
	}
}

func TestCompileGlobLimits(t *testing.T) {
	t.Parallel()

	if _, err := CompileGlob(strings.Repeat("a", MaxGlobLength+1)); !errors.Is(err, ErrInvalidGlob) {
		t.Errorf("over-long glob = %v, want ErrInvalidGlob", err)
	}
}

func mustPolicy(t *testing.T, m models.StoragePolicy) *Policy {
	t.Helper()

	m.Enabled = true

	p, err := CompilePolicy(&m)
	if err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}

	return p
}

func TestPolicyMatches(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	days := func(n int64) *int64 { return new(n * 86400) }

	base := Facts{SHA256: "s", SizeBytes: 1000, CreatedAt: now.Add(-40 * day), TierID: "hot"}
	withRefs := func(f Facts, refs ...models.BlobRef) *Facts { f.Refs = refs; return &f }
	idle := func(f Facts, d time.Duration) *Facts { f.LastAccessedAt = ago(d); return &f }

	for _, tc := range []struct {
		name   string
		policy models.StoragePolicy
		facts  *Facts
		want   bool
	}{
		{"age old enough", models.StoragePolicy{MinAgeSeconds: days(30)}, &base, true},
		{"age too young", models.StoragePolicy{MinAgeSeconds: days(60)}, &base, false},
		{"age boundary inclusive", models.StoragePolicy{MinAgeSeconds: days(40)}, &base, true},
		{"idle uses created when never read", models.StoragePolicy{MinIdleSeconds: days(30)}, &base, true},
		{"idle recently read", models.StoragePolicy{MinIdleSeconds: days(30)}, idle(base, 2*day), false},
		{"idle read long ago", models.StoragePolicy{MinIdleSeconds: days(30)}, idle(base, 35*day), true},
		{"read before creation is ignored", models.StoragePolicy{MinIdleSeconds: days(30)}, idle(base, 90*day), true},
		{"min size", models.StoragePolicy{MinSizeBytes: new(int64(1000))}, &base, true},
		{"below min size", models.StoragePolicy{MinSizeBytes: new(int64(1001))}, &base, false},
		{"max size", models.StoragePolicy{MaxSizeBytes: new(int64(1000))}, &base, true},
		{"above max size", models.StoragePolicy{MaxSizeBytes: new(int64(999))}, &base, false},
		{"from tier matches", models.StoragePolicy{MinAgeSeconds: days(1), FromTierID: "hot"}, &base, true},
		{"from tier differs", models.StoragePolicy{MinAgeSeconds: days(1), FromTierID: "cold"}, &base, false},
		{"repo glob", models.StoragePolicy{RepoGlob: "rel*"}, withRefs(base, models.BlobRef{Repo: "releases", Format: "maven", Path: "a/b"}), true},
		{"repo glob miss", models.StoragePolicy{RepoGlob: "rel*"}, withRefs(base, models.BlobRef{Repo: "snapshots", Format: "maven", Path: "a/b"}), false},
		{"format list", models.StoragePolicy{Formats: []string{"npm", "pypi"}}, withRefs(base, models.BlobRef{Repo: "r", Format: "pypi", Path: "p"}), true},
		{"format miss", models.StoragePolicy{Formats: []string{"npm"}}, withRefs(base, models.BlobRef{Repo: "r", Format: "pypi", Path: "p"}), false},
		{"path glob", models.StoragePolicy{PathGlob: "com/acme/**"}, withRefs(base, models.BlobRef{Repo: "r", Format: "maven", Path: "com/acme/x/1.0/x.jar"}), true},
		{"path glob miss", models.StoragePolicy{PathGlob: "com/acme/**"}, withRefs(base, models.BlobRef{Repo: "r", Format: "maven", Path: "org/x.jar"}), false},
		{"ref conditions are evaluated per ref", models.StoragePolicy{RepoGlob: "releases", Formats: []string{"npm"}},
			withRefs(base,
				models.BlobRef{Repo: "releases", Format: "maven", Path: "a"},
				models.BlobRef{Repo: "other", Format: "npm", Path: "b"}), false},
		{"any ref satisfying every ref condition", models.StoragePolicy{RepoGlob: "releases", Formats: []string{"npm"}},
			withRefs(base,
				models.BlobRef{Repo: "other", Format: "maven", Path: "a"},
				models.BlobRef{Repo: "releases", Format: "npm", Path: "b"}), true},
		{"ref condition without refs", models.StoragePolicy{RepoGlob: "releases"}, &base, false},
		{"combined", models.StoragePolicy{MinAgeSeconds: days(30), MinSizeBytes: new(int64(500)), Formats: []string{"maven"}},
			withRefs(base, models.BlobRef{Repo: "r", Format: "maven", Path: "p"}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := mustPolicy(t, tc.policy)

			if got := p.Matches(tc.facts, now); got != tc.want {
				t.Errorf("Matches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDisabledPolicyNeverMatches(t *testing.T) {
	t.Parallel()

	p := mustPolicy(t, models.StoragePolicy{MinAgeSeconds: new(int64(1))})
	p.Enabled = false

	f := &Facts{CreatedAt: time.Unix(0, 0)}
	if p.Matches(f, time.Now()) {
		t.Error("disabled policy matched")
	}
}

func TestDecideFirstMatchWins(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	f := &Facts{SHA256: "s", SizeBytes: 10, CreatedAt: now.Add(-100 * 24 * time.Hour), TierID: "hot"}

	small := mustPolicy(t, models.StoragePolicy{ID: "small", Position: 2, TargetTierID: "cold", MaxSizeBytes: new(int64(100))})
	old := mustPolicy(t, models.StoragePolicy{ID: "old", Position: 1, TargetTierID: "archive", MinAgeSeconds: new(int64(86400))})
	off := mustPolicy(t, models.StoragePolicy{ID: "off", Position: 0, TargetTierID: "x", MinAgeSeconds: new(int64(1))})
	off.Enabled = false

	policies := []*Policy{small, old, off}
	SortPolicies(policies)

	if got := Decide(policies, f, now); got == nil || got.ID != "old" {
		t.Fatalf("Decide = %+v, want policy old (lowest position among enabled matches)", got)
	}

	if got := Decide(nil, f, now); got != nil {
		t.Errorf("Decide without policies = %+v, want nil", got)
	}
}

func TestSortPoliciesByPositionThenName(t *testing.T) {
	t.Parallel()

	ps := []*Policy{{Name: "b", Position: 1}, {Name: "a", Position: 1}, {Name: "z", Position: 0}}
	SortPolicies(ps)

	if ps[0].Name != "z" || ps[1].Name != "a" || ps[2].Name != "b" {
		t.Errorf("order = %s %s %s", ps[0].Name, ps[1].Name, ps[2].Name)
	}
}

func TestScanClauseIsNecessaryConditionOfMatches(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	p := mustPolicy(t, models.StoragePolicy{
		TargetTierID: "cold", FromTierID: "hot",
		MinAgeSeconds: new(int64(86400)), MinIdleSeconds: new(int64(7200)),
		MinSizeBytes: new(int64(5)), MaxSizeBytes: new(int64(50)),
	})

	backends := map[string]string{"hot": "", "cold": "backend-cold"}
	c := p.ScanClause(now, func(id string) string { return backends[id] })

	if c.TargetBackendID != "backend-cold" || !c.HasFrom || c.FromBackendID != "" {
		t.Errorf("clause routing = %+v", c)
	}

	if c.CreatedBefore == nil || !c.CreatedBefore.Equal(now.Add(-24*time.Hour)) {
		t.Errorf("CreatedBefore = %v", c.CreatedBefore)
	}

	if c.IdleBefore == nil || !c.IdleBefore.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("IdleBefore = %v", c.IdleBefore)
	}

	if c.MinSizeBytes == nil || *c.MinSizeBytes != 5 || c.MaxSizeBytes == nil || *c.MaxSizeBytes != 50 {
		t.Errorf("size bounds = %v %v", c.MinSizeBytes, c.MaxSizeBytes)
	}
}
