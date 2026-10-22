// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const goList1 = "example.com/!mod/@v/list"

func goGroup(t *testing.T) *fixture {
	t.Helper()
	return newFixture(t, "go", member("hosted", "go", "hosted"), member("mirror", "go", "proxy"))
}

func TestGoGroup_ListIsTheUnionOfTheMembers(t *testing.T) {
	f := goGroup(t)
	f.set("hosted", goList1, 200, "v1.0.0\nv1.10.0\nv2.0.0-rc.1")
	f.set("mirror", goList1, 200, "v1.0.0\nv1.2.0\nv1.10.0\nv2.0.0+incompatible\njunk\n")

	w := f.get(http.MethodGet, goList1)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	// Semantic order, highest first (v1.10.0 above v1.2.0), duplicates once,
	// non-versions dropped.
	want := []string{"v2.0.0+incompatible", "v2.0.0-rc.1", "v1.10.0", "v1.2.0", "v1.0.0"}
	if got := strings.Split(w.Body.String(), "\n"); !reflect.DeepEqual(got, want) {
		t.Errorf("list = %q, want %q", got, want)
	}

	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}

	if w.Header().Get("ETag") == "" {
		t.Error("merged list has no validator")
	}
}

func TestGoGroup_ListAnsweredByOneMemberOrFailing(t *testing.T) {
	cases := []struct {
		name         string
		hosted, mirr [2]any // status, body
		want         int
		wantBody     string
	}{
		{"only the proxy knows the module", [2]any{404, ""}, [2]any{200, "v1.0.0"}, 200, "v1.0.0"},
		{"a failing member is skipped while another answers", [2]any{200, "v1.1.0"}, [2]any{503, "down"}, 200, "v1.1.0"},
		{"an empty list is an answer", [2]any{404, ""}, [2]any{200, ""}, 200, ""},
		{"nobody knows the module", [2]any{404, ""}, [2]any{404, ""}, 404, ""},
		{"nobody answers and one failed", [2]any{404, ""}, [2]any{502, "bad"}, 502, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := goGroup(t)
			f.set("hosted", goList1, c.hosted[0].(int), c.hosted[1].(string))
			f.set("mirror", goList1, c.mirr[0].(int), c.mirr[1].(string))

			w := f.get(http.MethodGet, goList1)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d", w.Code, c.want)
			}

			if c.want == 200 && w.Body.String() != c.wantBody {
				t.Errorf("body %q, want %q", w.Body.String(), c.wantBody)
			}
		})
	}
}

func TestGoGroup_ListHonorsAuthorizationAndConditionalRequests(t *testing.T) {
	f := goGroup(t)
	f.set("hosted", goList1, 200, "v1.0.0")
	f.set("mirror", goList1, 200, "v1.1.0")

	first := f.get(http.MethodGet, goList1)

	// Members that may not be read by the caller do not contribute.
	f.denied["mirror"] = true

	if w := f.get(http.MethodGet, goList1); w.Body.String() != "v1.0.0" {
		t.Errorf("list with the mirror denied = %q, want v1.0.0", w.Body.String())
	}

	f.denied["mirror"] = false

	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)

	w := httptest.NewRecorder()
	f.h.Serve(w, req, f.grp, goList1, f.backend())

	if w.Code != http.StatusNotModified {
		t.Errorf("conditional GET: status %d, want 304", w.Code)
	}
}

func TestGoGroup_OtherPathsAreFirstHit(t *testing.T) {
	f := goGroup(t)
	zipPath := "example.com/!mod/@v/v1.0.0.zip"
	f.set("hosted", zipPath, 200, "hosted-zip")
	f.set("mirror", zipPath, 200, "mirror-zip")
	f.set("mirror", "example.com/!mod/@v/v1.1.0.info", 200, "{}")
	f.set("hosted", "example.com/!mod/@latest", 200, "hosted-latest")
	f.set("mirror", "example.com/!mod/@latest", 200, "mirror-latest")

	cases := map[string]string{
		zipPath:                           "hosted-zip",
		"example.com/!mod/@v/v1.1.0.info": "{}",
		"example.com/!mod/@latest":        "hosted-latest",
	}

	for path, want := range cases {
		if w := f.get(http.MethodGet, path); w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", path, w.Code, w.Body.String(), want)
		}
	}

	if w := f.get(http.MethodGet, "example.com/!mod/@v/v9.9.9.zip"); w.Code != http.StatusNotFound {
		t.Errorf("unknown version: status %d, want 404", w.Code)
	}
}

func TestGoGroup_IsReadOnly(t *testing.T) {
	f := goGroup(t)

	for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		if w := f.get(m, "example.com/!mod/@v/v1.0.0.zip"); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", m, w.Code)
		}
	}
}
