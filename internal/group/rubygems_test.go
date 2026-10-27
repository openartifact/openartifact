// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/compactindex"
)

func gemInfo(lines ...string) string { return "---\n" + strings.Join(lines, "\n") + "\n" }

func gemLine(version, sumDigit string) string {
	return version + " |checksum:" + strings.Repeat(sumDigit, 64)
}

func gemVersions(created string, lines ...string) string {
	return "created_at: " + created + "\n---\n" + strings.Join(lines, "\n") + "\n"
}

func gemMD5(body string) string { return compactindex.InfoMD5([]byte(body)) }

func rubygemsGroup(t *testing.T, mergeProxy bool) *fixture {
	t.Helper()

	f := newFixture(t, "rubygems", member("hosted", "rubygems", "hosted"), member("mirror", "rubygems", "proxy"))
	f.grp.MergeProxyVersions = mergeProxy

	return f
}

func TestRubyGemsGroup_InfoHostedWinsAndMerge(t *testing.T) {
	hosted := gemInfo(gemLine("1.0.0", "a"))
	mirror := gemInfo(gemLine("1.0.0", "b"), gemLine("9.9.9", "b"))

	tests := []struct {
		name       string
		mergeProxy bool
		path       string
		want       string
		generated  bool // the group builds the response (else a single member answers it)
	}{
		{"hosted name hides the proxy", false, "info/internal", hosted, false},
		{"proxy only name is served verbatim", false, "info/rails", mirror, true},
		{"merge: union, hosted wins the duplicate", true, "info/internal", gemInfo(gemLine("1.0.0", "a"), gemLine("9.9.9", "b")), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := rubygemsGroup(t, tt.mergeProxy)
			f.set("hosted", "info/internal", 200, hosted)
			f.set("mirror", "info/internal", 200, mirror)
			f.set("mirror", "info/rails", 200, mirror)

			w := f.get(http.MethodGet, tt.path)
			if w.Code != http.StatusOK || w.Body.String() != tt.want {
				t.Fatalf("= %d %q, want %q", w.Code, w.Body.String(), tt.want)
			}

			if !tt.generated {
				return
			}

			if got, want := w.Header().Get("ETag"), `"`+gemMD5(tt.want)+`"`; got != want {
				t.Errorf("ETag %q, want %q", got, want)
			}

			if w.Header().Get("Repr-Digest") == "" || w.Header().Get("Digest") != w.Header().Get("Repr-Digest") {
				t.Errorf("digest headers: %v", w.Header())
			}
		})
	}
}

func TestRubyGemsGroup_InfoNotFoundAndMemberFailure(t *testing.T) {
	f := rubygemsGroup(t, false)
	if w := f.get(http.MethodGet, "info/none"); w.Code != http.StatusNotFound {
		t.Errorf("unknown gem = %d, want 404", w.Code)
	}

	// A hosted member that cannot say whether it holds the gem blocks the proxies.
	f.set("hosted", "info/x", 500, "boom")
	f.set("mirror", "info/x", 200, gemInfo(gemLine("1.0.0", "c")))

	if w := f.get(http.MethodGet, "info/x"); w.Code == http.StatusOK {
		t.Errorf("undecided ownership served %q", w.Body.String())
	}

	// Between two hosted members one failing leaves the other's answer, uncacheable.
	f = newFixture(t, "rubygems", member("h1", "rubygems", "hosted"), member("h2", "rubygems", "hosted"))
	f.set("h1", "info/x", 500, "boom")

	if w := f.get(http.MethodGet, "info/x"); w.Code != http.StatusBadGateway {
		t.Errorf("all members failing = %d, want 502", w.Code)
	}

	f.set("h2", "info/x", 200, gemInfo(gemLine("1.0.0", "c")))

	w := f.get(http.MethodGet, "info/x")
	if w.Code != http.StatusOK || w.Header().Get("ETag") != "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("partial = %d etag %q cache-control %q", w.Code, w.Header().Get("ETag"), w.Header().Get("Cache-Control"))
	}
}

func TestRubyGemsGroup_VersionsHostedWinsAndInfoConsistency(t *testing.T) {
	hostedInfo := gemInfo(gemLine("1.0.0", "a"))
	mirrorInfo := gemInfo(gemLine("1.0.0", "b"), gemLine("9.9.9", "b"))
	railsInfo := gemInfo(gemLine("7.0.0", "c"))
	hostedVersions := gemVersions("2024-01-01T00:00:00Z", "internal 1.0.0 "+gemMD5(hostedInfo))
	mirrorVersions := gemVersions("2024-06-01T00:00:00Z",
		"internal 1.0.0,9.9.9 "+gemMD5(mirrorInfo), "rails 7.0.0 "+gemMD5(railsInfo))

	setup := func(mergeProxy bool) *fixture {
		f := rubygemsGroup(t, mergeProxy)
		f.set("hosted", "versions", 200, hostedVersions)
		f.set("mirror", "versions", 200, mirrorVersions)
		f.set("hosted", "info/internal", 200, hostedInfo)
		f.set("mirror", "info/internal", 200, mirrorInfo)
		f.set("mirror", "info/rails", 200, railsInfo)

		return f
	}

	t.Run("hosted wins", func(t *testing.T) {
		w := setup(false).get(http.MethodGet, "versions")
		want := gemVersions("2024-06-01T00:00:00Z", "internal 1.0.0 "+gemMD5(hostedInfo), "rails 7.0.0 "+gemMD5(railsInfo))

		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("= %d %q, want %q", w.Code, w.Body.String(), want)
		}
	})

	t.Run("merged line carries the MD5 of the merged info file", func(t *testing.T) {
		f := setup(true)
		merged := f.get(http.MethodGet, "info/internal")
		w := f.get(http.MethodGet, "versions")

		want := gemVersions("2024-06-01T00:00:00Z",
			"internal 1.0.0,9.9.9 "+gemMD5(merged.Body.String()), "rails 7.0.0 "+gemMD5(railsInfo))
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("= %d %q, want %q", w.Code, w.Body.String(), want)
		}
	})
}

func TestRubyGemsGroup_NamesUnionAndSpecsNotServed(t *testing.T) {
	f := rubygemsGroup(t, false)
	f.set("hosted", "names", 200, "---\ninternal\nshared\n")
	f.set("mirror", "names", 200, "---\nrails\nshared\n")

	w := f.get(http.MethodGet, "names")
	if w.Code != http.StatusOK || w.Body.String() != "---\ninternal\nrails\nshared\n" {
		t.Errorf("names = %d %q", w.Code, w.Body.String())
	}

	f.set("mirror", "specs.4.8.gz", 200, "x")

	if w := f.get(http.MethodGet, "specs.4.8.gz"); w.Code != http.StatusNotFound {
		t.Errorf("legacy specs = %d, want 404", w.Code)
	}
}

func TestRubyGemsGroup_GemFileOfHostedNameIsNeverTheProxys(t *testing.T) {
	f := rubygemsGroup(t, false)
	// "a-1-2.gem" reads as gem a-1 version 2 or gem a version 1-2.
	f.set("hosted", "info/a-1", 200, gemInfo(gemLine("3.0.0", "a")))
	f.set("mirror", "gems/a-1-2.gem", 200, "proxy-bytes")

	if w := f.get(http.MethodGet, "gems/a-1-2.gem"); w.Code != http.StatusNotFound {
		t.Errorf("shadowed gem = %d %q, want 404", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, "gems/other-1.0.gem"); w.Code != http.StatusNotFound {
		t.Errorf("unknown gem = %d, want 404", w.Code)
	}

	f.set("mirror", "gems/other-1.0.gem", 200, "proxy-bytes")

	if w := f.get(http.MethodGet, "gems/other-1.0.gem"); w.Code != http.StatusOK || w.Body.String() != "proxy-bytes" {
		t.Errorf("proxy-only gem = %d %q", w.Code, w.Body.String())
	}
}

func TestRubyGemsGroup_SingleMemberPassesThrough(t *testing.T) {
	f := newFixture(t, "rubygems", member("mirror", "rubygems", "proxy"))
	f.set("mirror", "versions", 200, "raw-bytes")

	if w := f.get(http.MethodGet, "versions"); w.Code != http.StatusOK || w.Body.String() != "raw-bytes" {
		t.Errorf("= %d %q", w.Code, w.Body.String())
	}
}
