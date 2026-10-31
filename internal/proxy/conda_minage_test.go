// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestCondaTooNew(t *testing.T) {
	now := time.Date(2025, 6, 2, 12, 0, 0, 0, time.UTC)
	drop := condaTooNew(&models.Repository{MinAgeHours: 24}, now)

	ms := func(t time.Time) string { return fmt.Sprintf(`{"timestamp":%d}`, t.UnixMilli()) }
	sec := func(t time.Time) string { return fmt.Sprintf(`{"timestamp":%d}`, t.Unix()) }

	threshold := now.Add(-24 * time.Hour)

	cases := []struct {
		name  string
		entry string
		want  bool
	}{
		{"milliseconds, a millisecond short", ms(threshold.Add(time.Millisecond)), true},
		{"milliseconds, exactly at the threshold", ms(threshold), false},
		{"seconds, a second short", sec(threshold.Add(time.Second)), true},
		{"seconds, exactly at the threshold", sec(threshold), false},
		{"seconds, long ago", `{"timestamp":1500000000}`, false},
		{"no timestamp", `{"name":"x"}`, false},
		{"timestamp zero", `{"timestamp":0}`, false},
		{"timestamp not a number", `{"timestamp":"soon"}`, false},
		{"timestamp a number in a string", `{"timestamp":"` + strconv.FormatInt(now.UnixMilli(), 10) + `"}`, true},
		{"not an object", `[]`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := drop(json.RawMessage(c.entry)); got != c.want {
				t.Errorf("dropped = %v, want %v", got, c.want)
			}
		})
	}

	if condaTooNew(&models.Repository{}, now)(json.RawMessage(ms(now))) {
		t.Error("gate 0 dropped an entry")
	}
}

// condaGateFixture is condaFixture with the gate on 24 hours and a repodata
// whose demo package was built an hour ago (milliseconds) and old package three
// days ago (seconds).
func condaGateFixture(t *testing.T) (*fixture, *fakeConda) {
	t.Helper()

	f, up := condaFixture(t)
	f.repo.MinAgeHours = 24

	now := time.Now()
	entry := func(name, sha, md5sum string, size int64, ts string) string {
		e := condaEntryJSON(name, sha, md5sum, size)
		return strings.TrimSuffix(e, "}") + `,"timestamp":` + ts + "}"
	}

	repodata := condaRepodata(`{"subdir":"linux-64"}`,
		map[string]string{condaOldPkg: entry("old", "", condaMD5Hex(condaOldPkgBody), int64(len(condaOldPkgBody)),
			strconv.FormatInt(now.Add(-72*time.Hour).Unix(), 10))},
		map[string]string{condaPkg: entry("demo", sum(string(condaPkgBody)), condaMD5Hex(condaPkgBody), int64(len(condaPkgBody)),
			strconv.FormatInt(now.Add(-time.Hour).UnixMilli(), 10))})

	up.set("linux-64/repodata.json", repodata)
	up.set("linux-64/repodata.json.zst", zstdOf(t, repodata))
	up.set("linux-64/current_repodata.json", repodata)

	return f, up
}

func condaPackagesOf(t *testing.T, doc []byte) ([]string, []string) {
	t.Helper()

	var parsed struct {
		Info     map[string]string          `json:"info"`
		Packages map[string]json.RawMessage `json:"packages"`
		Conda    map[string]json.RawMessage `json:"packages.conda"` //nolint:tagliatelle // conda wire format
	}

	if err := json.Unmarshal(doc, &parsed); err != nil {
		t.Fatalf("not repodata: %v\n%s", err, doc)
	}

	if parsed.Info["subdir"] != "linux-64" {
		t.Errorf("info = %v, want the subdir", parsed.Info)
	}

	var tarbz2, condas []string

	for k := range parsed.Packages {
		tarbz2 = append(tarbz2, k)
	}

	for k := range parsed.Conda {
		condas = append(condas, k)
	}

	return tarbz2, condas
}

func TestCondaGate_RepodataLeavesOutTooNewPackages(t *testing.T) {
	f, _ := condaGateFixture(t)

	for _, path := range []string{"linux-64/repodata.json", "linux-64/current_repodata.json"} {
		w := f.do(http.MethodGet, path)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: %d %q", path, w.Code, w.Header().Get("Content-Type"))
		}

		tarbz2, condas := condaPackagesOf(t, w.Body.Bytes())
		if len(tarbz2) != 1 || tarbz2[0] != condaOldPkg || len(condas) != 0 {
			t.Errorf("%s: packages %v, packages.conda %v; want only %s", path, tarbz2, condas, condaOldPkg)
		}

		if w.Header().Get("ETag") == "" {
			t.Errorf("%s: no validator", path)
		}
	}

	// The compressed copy is the same document, zstd-compressed.
	w := f.do(http.MethodGet, "linux-64/repodata.json.zst")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zstd" {
		t.Fatalf("zst: %d %q", w.Code, w.Header().Get("Content-Type"))
	}

	dec, err := zstd.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	plain, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("zstd body: %v", err)
	}

	if tarbz2, condas := condaPackagesOf(t, plain); len(tarbz2) != 1 || len(condas) != 0 {
		t.Errorf("zst: packages %v, packages.conda %v", tarbz2, condas)
	}

	if w := f.do(http.MethodHead, "linux-64/repodata.json"); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d bytes", w.Code, w.Body.Len())
	}

	// The bz2 copy cannot be produced.
	if w := f.do(http.MethodGet, "linux-64/repodata.json.bz2"); w.Code != http.StatusNotFound {
		t.Errorf("bz2: %d, want 404", w.Code)
	}
}

func TestCondaGate_PackageOfATooNewEntryIsRefusedAndNotFetched(t *testing.T) {
	f, up := condaGateFixture(t)

	if w := f.do(http.MethodGet, "linux-64/"+condaPkg); w.Code != http.StatusForbidden {
		t.Fatalf("too new package: %d, want 403", w.Code)
	}

	if up.hitsOf("linux-64/"+condaPkg) != 0 {
		t.Error("a too new package was fetched from the upstream")
	}

	if f.db.entry("repo-1", "linux-64/"+condaPkg) != nil {
		t.Error("a too new package was cached")
	}

	if w := f.do(http.MethodGet, "linux-64/"+condaOldPkg); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), condaOldPkgBody) {
		t.Errorf("old enough package: %d", w.Code)
	}
}

func TestCondaGate_ValidatorDependsOnTheGate(t *testing.T) {
	f, _ := condaGateFixture(t)

	first := f.do(http.MethodGet, "linux-64/repodata.json").Header().Get("ETag")

	f.repo.MinAgeHours = 48

	if second := f.do(http.MethodGet, "linux-64/repodata.json").Header().Get("ETag"); first == "" || first == second {
		t.Errorf("ETags %q and %q must differ across gate settings", first, second)
	}
}

func TestCondaGate_GarbageUpstreamRepodataIsRefused(t *testing.T) {
	f, up := condaGateFixture(t)
	up.set("linux-64/repodata.json", []byte(`{"packages": 3}`))

	if w := f.do(http.MethodGet, "linux-64/repodata.json"); w.Code == http.StatusOK {
		t.Fatalf("garbage repodata served: %d", w.Code)
	}
}
