// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const cargoBase = "http://oa.test"

func cargoLine(member, vers string) string {
	b, _ := json.Marshal(map[string]any{"name": "demo", "vers": vers, "cksum": "sum-" + member, "deps": []any{}})
	return string(b) + "\n"
}

func cargoGroup(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, "cargo", member("hosted", "cargo", "hosted"), member("mirror", "cargo", "proxy"))
	f.h.SetBaseURL(cargoBase + "/")

	return f
}

func TestCargoGroup_IndexIsTheUnionFirstMemberWins(t *testing.T) {
	f := cargoGroup(t)
	f.set("hosted", "index/de/mo/demo", 200, cargoLine("hosted", "1.0.0"))
	f.set("mirror", "index/de/mo/demo", 200, cargoLine("mirror", "1.0.0")+cargoLine("mirror", "2.0.0"))

	w := f.get(http.MethodGet, "index/de/mo/demo")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	want := cargoLine("hosted", "1.0.0") + cargoLine("mirror", "2.0.0")
	if w.Body.String() != want {
		t.Errorf("merged index:\n%s\nwant:\n%s", w.Body.String(), want)
	}

	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("merged index has no ETag")
	}

	// The same merge yields the same validator, so clients can revalidate.
	f2 := cargoGroup(t)
	f2.replies = f.replies

	if got := f2.get(http.MethodGet, "index/de/mo/demo").Header().Get("ETag"); got != etag {
		t.Errorf("ETag is not stable across identical merges: %q vs %q", got, etag)
	}
}

func TestCargoGroup_MemberWithoutTheCrateIsSkipped(t *testing.T) {
	f := cargoGroup(t)
	f.set("mirror", "index/de/mo/demo", 200, cargoLine("mirror", "1.0.0"))

	if w := f.get(http.MethodGet, "index/de/mo/demo"); w.Code != http.StatusOK || w.Body.String() != cargoLine("mirror", "1.0.0") {
		t.Errorf("= %d %q", w.Code, w.Body.String())
	}

	if w := f.get(http.MethodGet, "index/ab/cd/abcd"); w.Code != http.StatusNotFound {
		t.Errorf("unknown crate = %d, want 404", w.Code)
	}
}

func TestCargoGroup_FailingMemberDoesNotHideTheOthersButAloneIs502(t *testing.T) {
	f := cargoGroup(t)
	f.set("hosted", "index/de/mo/demo", 500, "boom")
	f.set("mirror", "index/de/mo/demo", 200, cargoLine("mirror", "1.0.0"))

	if w := f.get(http.MethodGet, "index/de/mo/demo"); w.Code != http.StatusOK {
		t.Errorf("one failing member = %d, want the others' merge", w.Code)
	}

	f.set("mirror", "index/de/mo/demo", 502, "boom")

	if w := f.get(http.MethodGet, "index/de/mo/demo"); w.Code != http.StatusBadGateway {
		t.Errorf("every member failing = %d, want 502", w.Code)
	}
}

func TestCargoGroup_NotAnIndexFileIs502(t *testing.T) {
	f := cargoGroup(t)
	f.set("mirror", "index/de/mo/demo", 200, "<html>portal</html>")

	if w := f.get(http.MethodGet, "index/de/mo/demo"); w.Code != http.StatusBadGateway {
		t.Errorf("= %d, want 502", w.Code)
	}
}

func TestCargoGroup_ConfigIsGeneratedWithoutAskingMembers(t *testing.T) {
	f := cargoGroup(t)

	for _, path := range []string{"config.json", "index/config.json"} {
		w := f.get(http.MethodGet, path)

		var cfg map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s = %d %q", path, w.Code, w.Body.String())
		}

		if cfg["dl"] != cargoBase+"/repos/all/api/v1/crates" || cfg["api"] != cargoBase+"/repos/all" {
			t.Errorf("%s: %v", path, cfg)
		}

		if cfg["auth-required"] != true {
			t.Errorf("%s: a non-public group must require auth: %v", path, cfg)
		}
	}

	if len(f.asked) != 0 {
		t.Errorf("members asked for config.json: %v", f.asked)
	}
}

func TestCargoGroup_DownloadComesFromFirstMemberThatHasIt(t *testing.T) {
	f := cargoGroup(t)
	const p = "api/v1/crates/demo/1.0.0/download"
	f.set("mirror", p, 200, "mirror-crate")

	if w := f.get(http.MethodGet, p); w.Body.String() != "mirror-crate" {
		t.Errorf("= %d %q", w.Code, w.Body.String())
	}

	f.set("hosted", p, 200, "hosted-crate")

	if w := f.get(http.MethodGet, p); w.Body.String() != "hosted-crate" {
		t.Errorf("= %q, want the first member", w.Body.String())
	}
}

func TestCargoGroup_NonRegistryPathsAre404WithoutAskingMembers(t *testing.T) {
	f := cargoGroup(t)

	for _, p := range []string{"index/De/Mo/Demo", "api/v1/crates/new", "foo", "index/de/mo/../x"} {
		if w := f.get(http.MethodGet, p); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, w.Code)
		}
	}

	if len(f.asked) != 0 {
		t.Errorf("members asked: %v", f.asked)
	}
}

func TestCargoGroup_WritesAreRejected(t *testing.T) {
	f := cargoGroup(t)

	if w := f.get(
		http.MethodPut,
		"api/v1/crates/new",
	); w.Code != http.StatusMethodNotAllowed ||
		!strings.Contains(w.Header().Get("Allow"), "GET") {
		t.Errorf("PUT = %d Allow %q", w.Code, w.Header().Get("Allow"))
	}
}
