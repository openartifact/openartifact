// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/pacman"
)

func entry(name, version string) pacman.DBEntry {
	return pacman.DBEntry{Name: name, Version: version, Filename: name + "-" + version + "-x86_64.pkg.tar.zst", SHA256: name + version}
}

func pickMap(picks []pacmanPick) map[string]pacmanPick {
	out := make(map[string]pacmanPick, len(picks))
	for _, p := range picks {
		out[p.entry.Name] = p
	}

	return out
}

func TestMergePacman(t *testing.T) {
	t.Parallel()

	px := &models.Repository{ID: "px"}
	px2 := &models.Repository{ID: "px2"}

	contribs := []pacmanContribution{
		{member: "px", proxy: px, entries: []pacman.DBEntry{entry("tool", "9-1"), entry("only-proxy", "1-1")}},
		{member: "px2", proxy: px2, entries: []pacman.DBEntry{entry("only-proxy", "2-1"), entry("shared", "1-1")}},
		{member: "h", entries: []pacman.DBEntry{entry("tool", "1-1"), entry("shared", "0.5-1")}},
	}

	t.Run("hosted wins whatever the order", func(t *testing.T) {
		t.Parallel()

		got := pickMap(mergePacman(contribs, false))
		if len(got) != 3 {
			t.Fatalf("picks = %d, want 3: %+v", len(got), got)
		}

		for name, want := range map[string]struct{ member, version string }{
			"tool":       {"h", "1-1"},
			"shared":     {"h", "0.5-1"},
			"only-proxy": {"px", "1-1"}, // first proxy in order
		} {
			if got[name].member != want.member || got[name].entry.Version != want.version {
				t.Errorf("%s = %s %s, want %s %s", name, got[name].member, got[name].entry.Version, want.member, want.version)
			}
		}
	})

	t.Run("merge proxy versions picks the newest", func(t *testing.T) {
		t.Parallel()

		got := pickMap(mergePacman(contribs, true))
		for name, want := range map[string]struct{ member, version string }{
			"tool":       {"px", "9-1"},
			"shared":     {"px2", "1-1"},
			"only-proxy": {"px2", "2-1"},
		} {
			if got[name].member != want.member || got[name].entry.Version != want.version {
				t.Errorf("%s = %s %s, want %s %s", name, got[name].member, got[name].entry.Version, want.member, want.version)
			}
		}
	})

	t.Run("tie goes to the earlier member", func(t *testing.T) {
		t.Parallel()

		got := pickMap(mergePacman([]pacmanContribution{
			{member: "a", entries: []pacman.DBEntry{entry("x", "1-1")}},
			{member: "b", entries: []pacman.DBEntry{entry("x", "1-1")}},
		}, true))
		if got["x"].member != "a" {
			t.Errorf("member = %s, want a", got["x"].member)
		}
	})

	t.Run("sorted by name", func(t *testing.T) {
		t.Parallel()

		picks := mergePacman(contribs, false)
		for i := 1; i < len(picks); i++ {
			if picks[i-1].entry.Name > picks[i].entry.Name {
				t.Fatalf("not sorted: %v before %v", picks[i-1].entry.Name, picks[i].entry.Name)
			}
		}
	})
}

func TestProxySection(t *testing.T) {
	t.Parallel()

	for url, want := range map[string]string{
		"https://geo.mirror.pkgbuild.com/extra":  "extra",
		"https://geo.mirror.pkgbuild.com/extra/": "extra",
		"https://mirror.example/archlinux/core":  "core",
		"https://geo.mirror.pkgbuild.com":        "",
		"https://geo.mirror.pkgbuild.com/":       "",
		"https://mirror.example/with space":      "",
		"://bad":                                 "",
	} {
		got, ok := proxySection(&models.Repository{UpstreamURL: url})
		if got != want || ok != (want != "") {
			t.Errorf("proxySection(%q) = %q, %v; want %q", url, got, ok, want)
		}
	}
}
