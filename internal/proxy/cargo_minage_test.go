// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

const cargoGateIndex = "index/de/mo/demo"

func cargoGateLine(vers, pubtime string) string {
	if pubtime == "" {
		return `{"name":"demo","vers":"` + vers + `","cksum":"x"}`
	}

	return `{"name":"demo","vers":"` + vers + `","cksum":"x","pubtime":"` + pubtime + `"}`
}

func TestFilterCargoIndex(t *testing.T) {
	now := time.Date(2025, 11, 12, 12, 0, 0, 0, time.UTC)
	repo := &models.Repository{MinAgeHours: 24}

	doc := func(lines ...string) []byte { return []byte(strings.Join(lines, "\n") + "\n") }

	cases := []struct {
		name    string
		repo    *models.Repository
		doc     []byte
		want    []string // versions kept
		wantErr error
	}{
		{
			name: "too new versions are left out",
			repo: repo,
			doc: doc(cargoGateLine("1.0.0", "2025-11-01T00:00:00Z"), cargoGateLine("1.1.0", "2025-11-12T00:00:00Z"),
				cargoGateLine("1.2.0", "2025-11-12T11:59:59Z")),
			want: []string{"1.0.0"},
		},
		{
			name: "exactly at the threshold is old enough, a second short is not",
			repo: repo,
			doc:  doc(cargoGateLine("1.0.0", "2025-11-11T12:00:00Z"), cargoGateLine("1.1.0", "2025-11-11T12:00:01Z")),
			want: []string{"1.0.0"},
		},
		{
			name: "an entry without a pubtime or with an unreadable one passes",
			repo: repo,
			doc:  doc(cargoGateLine("1.0.0", ""), cargoGateLine("1.1.0", "yesterday"), cargoGateLine("1.2.0", "2025-11-12T11:00:00Z")),
			want: []string{"1.0.0", "1.1.0"},
		},
		{
			name:    "every version too new is a crate without versions",
			repo:    repo,
			doc:     doc(cargoGateLine("1.0.0", "2025-11-12T11:00:00Z")),
			wantErr: errNotFound,
		},
		{
			name: "gate 0 keeps everything",
			repo: &models.Repository{},
			doc:  doc(cargoGateLine("1.0.0", "2025-11-12T11:00:00Z"), cargoGateLine("1.1.0", "")),
			want: []string{"1.0.0", "1.1.0"},
		},
		{name: "not an index", repo: repo, doc: []byte("<html>"), wantErr: errUnusableDoc},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := filterCargoIndex(newGate(c.repo, nil, now), "demo", c.doc)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}

			if c.wantErr != nil {
				return
			}

			var want strings.Builder

			for _, v := range c.want {
				for line := range strings.SplitSeq(string(c.doc), "\n") {
					if strings.Contains(line, `"vers":"`+v+`"`) {
						want.WriteString(line + "\n")
					}
				}
			}

			if string(out) != want.String() {
				t.Errorf("kept\n%s\nwant\n%s", out, want.String())
			}
		})
	}
}

func TestCargoGate_IndexAndCrateOfATooNewVersion(t *testing.T) {
	f, g := cargoFixture(t)
	f.repo.MinAgeHours = 24

	now := time.Now()
	g.pubtime = map[string]string{
		"demo/1.0.0": now.Add(-72 * time.Hour).UTC().Format(time.RFC3339),
		"demo/1.1.0": now.Add(-time.Hour).UTC().Format(time.RFC3339),
	}

	w := f.do(http.MethodGet, cargoGateIndex)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"vers":"1.0.0"`) || strings.Contains(body, `"vers":"1.1.0"`) {
		t.Fatalf("index = %d %q, want 1.0.0 only", w.Code, w.Body.String())
	}

	if w := f.do(http.MethodGet, "api/v1/crates/demo/1.1.0/download"); w.Code != http.StatusForbidden {
		t.Errorf("too new crate = %d, want 403", w.Code)
	}

	if len(g.dlPaths) != 0 {
		t.Errorf("static host was asked for a too new crate: %v", g.dlPaths)
	}

	if e := f.db.entry("repo-1", "api/v1/crates/demo/1.1.0/download"); e != nil {
		t.Errorf("too new crate was cached: %+v", e)
	}

	if w := f.do(http.MethodGet, "api/v1/crates/demo/1.0.0/download"); w.Code != http.StatusOK || w.Body.String() != "demo 1.0.0 crate" {
		t.Errorf("old enough crate = %d %q", w.Code, w.Body.String())
	}
}

func TestCargoGate_AllTooNewIsNotFoundAndValidatorFollowsTheGate(t *testing.T) {
	f, g := cargoFixture(t)
	f.repo.MinAgeHours = 24

	young := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	g.pubtime = map[string]string{"demo/1.0.0": young, "demo/1.1.0": young}

	if w := f.do(http.MethodGet, cargoGateIndex); w.Code != http.StatusNotFound {
		t.Fatalf("index with every version too new = %d, want 404", w.Code)
	}

	// The validator of a gated index is not the verbatim one, and changes with
	// the gate setting.
	g.pubtime = nil

	f.db.expire("repo-1", cargoGateIndex)

	gated := f.do(http.MethodGet, cargoGateIndex)
	if gated.Code != http.StatusOK {
		t.Fatalf("index = %d", gated.Code)
	}

	f.repo.MinAgeHours = 48

	other := f.do(http.MethodGet, cargoGateIndex)
	if gated.Header().Get("ETag") == "" || gated.Header().Get("ETag") == other.Header().Get("ETag") {
		t.Errorf("ETags %q and %q must differ across gate settings", gated.Header().Get("ETag"), other.Header().Get("ETag"))
	}
}

func TestCargoGate_OffServesTheIndexVerbatim(t *testing.T) {
	f, g := cargoFixture(t)
	g.pubtime = map[string]string{"demo/1.1.0": time.Now().UTC().Format(time.RFC3339)}

	w := f.do(http.MethodGet, cargoGateIndex)
	if want := g.line("demo", "1.0.0") + "\n" + g.line("demo", "1.1.0") + "\n"; w.Code != http.StatusOK || w.Body.String() != want {
		t.Fatalf("index = %d %q", w.Code, w.Body.String())
	}

	if w := f.do(http.MethodGet, "api/v1/crates/demo/1.1.0/download"); w.Code != http.StatusOK {
		t.Errorf("crate = %d, want 200", w.Code)
	}
}
