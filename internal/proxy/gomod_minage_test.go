// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

const goGateBase = goEscMod1 + "/@v/"

// setInfo publishes the .info of version, dated at.
func (e *goEnv) setInfo(version string, at time.Time) {
	e.up.set(goGateBase+version+".info", []byte(`{"Version":"`+version+`","Time":"`+at.UTC().Format(time.RFC3339)+`"}`))
}

func (e *goEnv) setList(versions ...string) {
	e.up.set(goGateBase+"list", []byte(strings.Join(versions, "\n")+"\n"))
}

// getAt answers path as the service does at the fixed time now.
func (e *goEnv) getAt(path string, now time.Time) *httptest.ResponseRecorder {
	e.t.Helper()

	req, ok := parseGoPath(path)
	if !ok {
		e.t.Fatalf("not a go path: %s", path)
	}

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	if !e.svc.serveGoGated(w, r, e.repo, req, now) {
		e.svc.serveVerbatim(w, r, e.repo, e.svc.goDoc(e.repo, req, now))
	}

	return w
}

func (e *goEnv) upstreamSaw(suffix string) bool {
	return slices.ContainsFunc(e.up.paths, func(p string) bool { return strings.HasSuffix(p, suffix) })
}

func TestGoGate_InfoIsRefusedUntilItIsOldEnough(t *testing.T) {
	e := newGoEnv(t)
	e.repo.MinAgeHours = 24

	published := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	e.setInfo("v1.3.0", published)

	cases := []struct {
		name string
		now  time.Time
		want int
	}{
		{"a second short of the threshold", published.Add(24*time.Hour - time.Second), http.StatusForbidden},
		{"exactly at the threshold", published.Add(24 * time.Hour), http.StatusOK},
		{"past the threshold", published.Add(25 * time.Hour), http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := e.getAt(goGateBase+"v1.3.0.info", c.now)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d (%s)", w.Code, c.want, w.Body.String())
			}

			if c.want == http.StatusForbidden && !strings.Contains(w.Body.String(), "minimum age") {
				t.Errorf("body %q does not say why", w.Body.String())
			}
		})
	}
}

func TestGoGate_ModAndZipOfATooNewVersionAreRefusedAndNotFetched(t *testing.T) {
	e := newGoEnv(t)
	e.repo.MinAgeHours = 24
	e.setInfo("v1.3.0", time.Now().Add(-time.Hour))
	e.up.set(goGateBase+"v1.3.0.mod", []byte("module "+goMod1+"\n"))
	e.up.set(goGateBase+"v1.3.0.zip", []byte("zip"))

	for _, ext := range []string{".mod", ".zip"} {
		if w := e.get(goGateBase + "v1.3.0" + ext); w.Code != http.StatusForbidden {
			t.Fatalf("v1.3.0%s: status %d, want 403", ext, w.Code)
		}

		if e.db.entry("repo-1", goGateBase+"v1.3.0"+ext) != nil {
			t.Errorf("v1.3.0%s was cached", ext)
		}

		if e.upstreamSaw("/v1.3.0" + ext) {
			t.Errorf("v1.3.0%s was requested upstream", ext)
		}
	}

	// A version old enough is served as before.
	for path, want := range map[string]string{
		goGateBase + goVersion1 + ".mod": e.genuine.mod,
		goGateBase + goVersion1 + ".zip": string(e.genuine.zip),
	} {
		if w := e.get(path); w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("GET %s: status %d", path, w.Code)
		}
	}
}

func TestGoGate_CachedFilesAreServedAsTheyAre(t *testing.T) {
	e := newGoEnv(t)

	// Cached before the gate was set.
	if w := e.get(goGateBase + goVersion1 + ".zip"); w.Code != http.StatusOK {
		t.Fatalf("zip: status %d", w.Code)
	}

	e.repo.MinAgeHours = 1_000_000

	before := len(e.up.paths)

	if w := e.get(goGateBase + goVersion1 + ".zip"); w.Code != http.StatusOK {
		t.Fatalf("cached zip with the gate on: status %d", w.Code)
	}

	if len(e.up.paths) != before {
		t.Error("a cached zip asked the upstream again")
	}
}

func TestGoGate_ListHidesTooNewVersionsAndLooksNoFurtherThanTheNewestOldRelease(t *testing.T) {
	e := newGoEnv(t)
	e.repo.MinAgeHours = 24

	now := time.Now()
	e.setList("v1.0.0", goVersion1, "v1.3.0", "v1.4.0-rc.1", "v1.5.0")
	e.setInfo("v1.3.0", now.Add(-2*time.Hour))
	e.setInfo("v1.4.0-rc.1", now.Add(-time.Hour))
	e.setInfo("v1.5.0", now.Add(-time.Minute))

	w := e.get(goGateBase + "list")
	if w.Code != http.StatusOK || w.Body.String() != "v1.0.0\n"+goVersion1+"\n" {
		t.Fatalf("status %d, list %q", w.Code, w.Body.String())
	}

	// v1.2.3 is the newest release old enough: nothing below it was looked at.
	if e.upstreamSaw("/v1.0.0.info") {
		t.Error("the .info of a version below the newest old release was requested")
	}

	// Once the versions are old enough the same cached list shows them, under
	// another validator.
	later := e.getAt(goGateBase+"list", now.Add(48*time.Hour))
	if later.Body.String() != "v1.0.0\n"+goVersion1+"\nv1.3.0\nv1.4.0-rc.1\nv1.5.0\n" {
		t.Errorf("list after the versions aged: %q", later.Body.String())
	}

	if w.Header().Get("ETag") == "" || w.Header().Get("ETag") == later.Header().Get("ETag") {
		t.Errorf("validators %q and %q must differ when the listing does", w.Header().Get("ETag"), later.Header().Get("ETag"))
	}
}

func TestGoGate_ListKeepsUndatedVersionsAndIsUnchangedWithoutTheGate(t *testing.T) {
	e := newGoEnv(t)
	e.setList("v1.0.0", goVersion1, "v1.3.0")
	e.setInfo("v1.3.0", time.Now().Add(-time.Minute))

	// Gate off: the upstream's list, with no .info looked at.
	if w := e.get(goGateBase + "list"); w.Body.String() != "v1.0.0\n"+goVersion1+"\nv1.3.0\n" {
		t.Fatalf("list without the gate: %q", w.Body.String())
	}

	if e.upstreamSaw(".info") {
		t.Error("the gate is off and a .info was requested")
	}

	// An upstream that has no .info for a version cannot date it: it passes.
	e.repo.MinAgeHours = 24
	e.setList("v1.0.0", goVersion1, "v1.2.4", "v1.3.0")

	e.db.expire("repo-1", goGateBase+"list")

	if w := e.get(goGateBase + "list"); w.Body.String() != "v1.0.0\n"+goVersion1+"\nv1.2.4\n" {
		t.Fatalf("list with an undated version: %q", w.Body.String())
	}
}

func TestGoGate_LatestFallsBackToTheNewestOldVersion(t *testing.T) {
	e := newGoEnv(t)
	e.repo.MinAgeHours = 24

	now := time.Now()
	e.setList("v1.0.0", goVersion1, "v1.3.0")
	e.setInfo("v1.3.0", now.Add(-time.Hour))
	e.up.set(goEscMod1+"/@latest", []byte(`{"Version":"v1.3.0","Time":"`+now.Add(-time.Hour).UTC().Format(time.RFC3339)+`"}`))

	w := e.get(goEscMod1 + "/@latest")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"Version":"`+goVersion1+`"`) {
		t.Fatalf("@latest: status %d, body %q", w.Code, w.Body.String())
	}

	// Old enough at a later time: the upstream's own answer.
	later := e.getAt(goEscMod1+"/@latest", now.Add(48*time.Hour))
	if !strings.Contains(later.Body.String(), `"Version":"v1.3.0"`) {
		t.Errorf("@latest after the version aged: %q", later.Body.String())
	}
}

func TestGoGate_LatestWithNothingOldEnoughIsNotFound(t *testing.T) {
	e := newGoEnv(t)
	e.repo.MinAgeHours = 24

	now := time.Now()
	e.setList("v1.3.0", "v1.4.0")
	e.setInfo("v1.3.0", now.Add(-2*time.Hour))
	e.setInfo("v1.4.0", now.Add(-time.Hour))
	e.up.set(goEscMod1+"/@latest", []byte(`{"Version":"v1.4.0","Time":"`+now.Add(-time.Hour).UTC().Format(time.RFC3339)+`"}`))

	if w := e.get(goEscMod1 + "/@latest"); w.Code != http.StatusNotFound {
		t.Errorf("@latest: status %d, want 404", w.Code)
	}

	if w := e.get(goGateBase + "list"); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("list: status %d, body %q, want an empty list", w.Code, w.Body.String())
	}
}

func TestGoGate_UndatedInfoPassesAndAQueryIsGatedByItsRevision(t *testing.T) {
	e := newGoEnv(t)
	e.repo.MinAgeHours = 24

	e.up.set(goGateBase+"v1.3.0.info", []byte(`{"Version":"v1.3.0"}`))

	if w := e.get(goGateBase + "v1.3.0.info"); w.Code != http.StatusOK {
		t.Errorf("undated info: status %d, want 200", w.Code)
	}

	e.up.set(goGateBase+"master.info",
		[]byte(`{"Version":"v1.3.1-0.20240101000000-abcdefabcdef","Time":"`+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)+`"}`))

	if w := e.get(goGateBase + "master.info"); w.Code != http.StatusForbidden {
		t.Errorf("a branch at a young revision: status %d, want 403", w.Code)
	}
}

func TestGoGate_ZeroGateIsUnchanged(t *testing.T) {
	e := newGoEnv(t)
	e.setInfo("v1.3.0", time.Now())

	if w := e.get(goGateBase + "v1.3.0.info"); w.Code != http.StatusOK {
		t.Errorf("info: status %d, want 200", w.Code)
	}
}
