// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/compactindex"
)

const gemAgeGate = 48 // hours

// gemAgeVersion is one version of the gems of gemAgeServer.
type gemAgeVersion struct {
	number, platform string
	hours            int // age; negative: not in the API
}

// field is the version field of the info file.
func (v gemAgeVersion) field() string {
	if v.platform == "" {
		return v.number
	}

	return v.number + "-" + v.platform
}

// gemAgeServer is a gem server with gems of known ages:
//
//	lib    1.0.0 200h, 1.1.0 72h, 2.0.0 1h, 2.0.0-java 1h, 3.0.0 in the info file
//	       only (the API document is older than the info file)
//	fresh  1.0.0 1h (the only version)
//	plain  1.0.0, a gem whose upstream serves no API
type gemAgeServer struct {
	t       *testing.T
	gems    map[string][]gemAgeVersion
	apiCode atomic.Int32 // status of the API, 0: normal
	gemGets atomic.Int32
	specGet atomic.Int32
}

func newGemAgeServer(t *testing.T) *gemAgeServer {
	t.Helper()

	return &gemAgeServer{t: t, gems: map[string][]gemAgeVersion{
		"lib": {
			{"1.0.0", "", 200}, {"1.1.0", "", 72}, {"2.0.0", "", 1}, {"2.0.0", "java", 1}, {"3.0.0", "", -1},
		},
		"fresh": {{"1.0.0", "", 1}},
		"plain": {{"1.0.0", "", -1}},
	}}
}

func (g *gemAgeServer) info(gem string) string {
	var b strings.Builder

	b.WriteString("---\n")

	for _, v := range g.gems[gem] {
		fmt.Fprintf(&b, "%s |checksum:%s\n", v.field(), gemSHA(gem+"-"+v.field()))
	}

	return b.String()
}

func (g *gemAgeServer) api(gem string) string {
	var parts []string

	for _, v := range g.gems[gem] {
		if v.hours < 0 {
			continue
		}

		platform := "ruby"
		if v.platform != "" {
			platform = v.platform
		}

		parts = append(parts, fmt.Sprintf(`{"number":%q,"platform":%q,"created_at":%q}`,
			v.number, platform, time.Now().Add(-time.Duration(v.hours)*time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")))
	}

	return "[" + strings.Join(parts, ",") + "]"
}

func (g *gemAgeServer) handler(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")

	switch {
	case strings.HasPrefix(p, "info/"):
		if _, ok := g.gems[strings.TrimPrefix(p, "info/")]; ok {
			_, _ = w.Write([]byte(g.info(strings.TrimPrefix(p, "info/"))))

			return
		}
	case strings.HasPrefix(p, "api/v1/versions/") && strings.HasSuffix(p, ".json"):
		gem := strings.TrimSuffix(strings.TrimPrefix(p, "api/v1/versions/"), ".json")
		if code := int(g.apiCode.Load()); code != 0 {
			http.Error(w, "api", code)
			return
		}

		if _, ok := g.gems[gem]; ok && gem != "plain" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(g.api(gem)))

			return
		}
	case strings.HasPrefix(p, "gems/") && strings.HasSuffix(p, ".gem"):
		g.gemGets.Add(1)
		_, _ = w.Write([]byte(strings.TrimSuffix(strings.TrimPrefix(p, "gems/"), ".gem")))

		return
	case strings.HasPrefix(p, "quick/Marshal.4.8/"):
		g.specGet.Add(1)

		var z bytes.Buffer

		zw := zlib.NewWriter(&z)
		_, _ = zw.Write([]byte{4, 8, '[', 0})
		_ = zw.Close()
		_, _ = w.Write(z.Bytes())

		return
	}

	http.NotFound(w, r)
}

func gemAgeFixture(t *testing.T, hours int) (*fixture, *gemAgeServer) {
	t.Helper()

	g := newGemAgeServer(t)
	f := newFixture(t, g.handler)
	f.repo.Format = "rubygems"
	f.repo.Name = "gems"
	f.repo.UpstreamURL = strings.TrimSuffix(f.repo.UpstreamURL, "/maven2/")
	f.repo.MinAgeHours = hours

	return f, g
}

// gemInfoLines are the version fields of an info file body.
func gemInfoLines(body string) []string {
	var out []string

	for l := range strings.SplitSeq(strings.TrimSpace(body), "\n") {
		if l != "---" {
			out = append(out, strings.Fields(l)[0])
		}
	}

	return out
}

func TestRubyGemsMinAge_InfoListsOnlyOldVersions(t *testing.T) {
	f, _ := gemAgeFixture(t, gemAgeGate)

	w := gemDo(f, http.MethodGet, "info/lib", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	// 2.0.0, 2.0.0-java (young) and 3.0.0 (not in the API yet) are left out.
	if got := strings.Join(gemInfoLines(w.Body.String()), " "); got != "1.0.0 1.1.0" {
		t.Errorf("versions = %q, want %q", got, "1.0.0 1.1.0")
	}

	if !strings.HasPrefix(w.Body.String(), "---\n") {
		t.Errorf("not an info file: %q", w.Body.String())
	}

	// The validators are those of the file served.
	body := w.Body.String()
	sha := sha256.Sum256([]byte(body))

	if want := `"` + compactindex.InfoMD5([]byte(body)) + `"`; w.Header().Get("ETag") != want {
		t.Errorf("ETag = %q, want %q", w.Header().Get("ETag"), want)
	}

	if want := `sha-256="` + base64.StdEncoding.EncodeToString(sha[:]) + `"`; w.Header().Get("Repr-Digest") != want {
		t.Errorf("Repr-Digest = %q, want %q", w.Header().Get("Repr-Digest"), want)
	}

	if n := gemDo(
		f,
		http.MethodGet,
		"info/lib",
		map[string]string{"If-None-Match": w.Header().Get("ETag")},
	).Code; n != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", n)
	}

	r := gemDo(f, http.MethodGet, "info/lib", map[string]string{"Range": "bytes=4-"})
	if r.Code != http.StatusPartialContent || r.Body.String() != body[4:] {
		t.Errorf("range = %d %q, want 206 %q", r.Code, r.Body.String(), body[4:])
	}
}

func TestRubyGemsMinAge_GateOffServesTheUpstreamFile(t *testing.T) {
	f, g := gemAgeFixture(t, 0)

	w := gemDo(f, http.MethodGet, "info/lib", nil)
	if w.Code != http.StatusOK || w.Body.String() != g.info("lib") {
		t.Errorf("gate off: %d %q, want the upstream file", w.Code, w.Body.String())
	}

	if w := gemDo(f, http.MethodGet, "gems/lib-2.0.0.gem", nil); w.Code != http.StatusOK {
		t.Errorf("gate off: young gem = %d, want 200", w.Code)
	}
}

func TestRubyGemsMinAge_NothingHiddenServesTheUpstreamFile(t *testing.T) {
	f, g := gemAgeFixture(t, 1)

	// Only the two old versions are left: nothing is too new, so the file is not
	// rewritten.
	g.gems["lib"] = g.gems["lib"][:2]

	w := gemDo(f, http.MethodGet, "info/lib", nil)
	if w.Code != http.StatusOK || w.Body.String() != g.info("lib") {
		t.Errorf("%d %q, want the upstream file untouched", w.Code, w.Body.String())
	}
}

func TestRubyGemsMinAge_GemWithOnlyYoungVersionsIsNotFound(t *testing.T) {
	f, _ := gemAgeFixture(t, gemAgeGate)

	if w := gemDo(f, http.MethodGet, "info/fresh", nil); w.Code != http.StatusNotFound {
		t.Errorf("info/fresh = %d, want 404", w.Code)
	}
}

func TestRubyGemsMinAge_UpstreamWithoutAPIPassesEverything(t *testing.T) {
	f, g := gemAgeFixture(t, gemAgeGate)

	w := gemDo(f, http.MethodGet, "info/plain", nil)
	if w.Code != http.StatusOK || w.Body.String() != g.info("plain") {
		t.Errorf("info/plain = %d %q, want the upstream file", w.Code, w.Body.String())
	}

	if w := gemDo(f, http.MethodGet, "gems/plain-1.0.0.gem", nil); w.Code != http.StatusOK {
		t.Errorf("plain gem = %d, want 200", w.Code)
	}
}

func TestRubyGemsMinAge_APIFailureIsNotPassedOff(t *testing.T) {
	f, g := gemAgeFixture(t, gemAgeGate)
	g.apiCode.Store(http.StatusInternalServerError)

	if w := gemDo(f, http.MethodGet, "info/lib", nil); w.Code != http.StatusBadGateway {
		t.Errorf("info with the API down = %d, want 502 (never an ungated file)", w.Code)
	}

	if w := gemDo(f, http.MethodGet, "gems/lib-1.0.0.gem", nil); w.Code == http.StatusOK {
		t.Errorf("gem with the API down = %d, want an error", w.Code)
	}
}

func TestRubyGemsMinAge_YoungGemsAreRefusedAndNeverFetched(t *testing.T) {
	f, g := gemAgeFixture(t, gemAgeGate)

	for _, file := range []string{
		"gems/lib-2.0.0.gem",
		"gems/lib-2.0.0-java.gem", // a platform gem
		"gems/lib-3.0.0.gem",      // the API does not know it yet
	} {
		if w := gemDo(f, http.MethodGet, file, nil); w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d %q, want 403", file, w.Code, w.Body.String())
		}
	}

	if got := g.gemGets.Load(); got != 0 {
		t.Errorf("the upstream gem was fetched %d times", got)
	}

	for _, file := range []string{"gems/lib-1.0.0.gem", "gems/lib-1.1.0.gem"} {
		if w := gemDo(f, http.MethodGet, file, nil); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d %q, want 200", file, w.Code, w.Body.String())
		}
	}
}

func TestRubyGemsMinAge_CachedGemIsServedAsIs(t *testing.T) {
	f, _ := gemAgeFixture(t, 0)

	if w := gemDo(f, http.MethodGet, "gems/lib-2.0.0.gem", nil); w.Code != http.StatusOK {
		t.Fatalf("ungated fetch = %d", w.Code)
	}

	f.repo.MinAgeHours = gemAgeGate

	if w := gemDo(f, http.MethodGet, "gems/lib-2.0.0.gem", nil); w.Code != http.StatusOK {
		t.Errorf("cached young gem = %d, want 200 (admitted before the gate)", w.Code)
	}
}

func TestRubyGemsMinAge_Gemspec(t *testing.T) {
	f, g := gemAgeFixture(t, gemAgeGate)

	if w := gemDo(f, http.MethodGet, "quick/Marshal.4.8/lib-2.0.0.gemspec.rz", nil); w.Code != http.StatusForbidden {
		t.Errorf("young gemspec = %d, want 403", w.Code)
	}

	if got := g.specGet.Load(); got != 0 {
		t.Errorf("the upstream gemspec was fetched %d times", got)
	}

	if w := gemDo(f, http.MethodGet, "quick/Marshal.4.8/lib-1.0.0.gemspec.rz", nil); w.Code != http.StatusOK {
		t.Errorf("old gemspec = %d, want 200", w.Code)
	}
}

func TestGateRubyGemsInfo_Boundaries(t *testing.T) {
	repo := &models.Repository{MinAgeHours: 24}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	lines, err := compactindex.ParseInfoBytes([]byte("---\n1.0.0 |x\n2.0.0 |x\n3.0.0 |x\n4.0.0 |x\n5.0.0-java |x\n"))
	if err != nil {
		t.Fatal(err)
	}

	dates := map[string]time.Time{
		"1.0.0":      now.Add(-24 * time.Hour),                 // exactly the threshold: old enough
		"2.0.0":      now.Add(-24*time.Hour + time.Nanosecond), // just short
		"3.0.0":      {},                                       // listed without a readable time: passes
		"5.0.0-java": now.Add(-100 * time.Hour),
		// 4.0.0 is not listed: too new
	}

	var got []string
	for _, l := range gateRubyGemsInfo(newGate(repo, nil, now), "lib", lines, dates) {
		got = append(got, l.Version)
	}

	if want := "1.0.0 3.0.0 5.0.0-java"; strings.Join(got, " ") != want {
		t.Errorf("kept %v, want %s", got, want)
	}
}

func TestParseGemDates(t *testing.T) {
	doc := []byte(`[
		{"number":"1.0.0","platform":"ruby","created_at":"2024-01-02T03:04:05.678Z"},
		{"number":"1.0.0","platform":"x86_64-linux","created_at":"2024-01-03T00:00:00.000Z"},
		{"number":"2.0.0","platform":"ruby","created_at":"2024-02-01T00:00:00.000Z","ruby_abi":"3.3"},
		{"number":"2.0.0","platform":"ruby","created_at":"2024-02-05T00:00:00.000Z","ruby_abi":"3.4"},
		{"number":"3.0.0","platform":"ruby","created_at":"garbage"}]`)

	dates, err := parseGemDates(doc)
	if err != nil {
		t.Fatal(err)
	}

	if len(dates) != 4 {
		t.Fatalf("dates = %v, want 4 entries", dates)
	}

	if got := dates["1.0.0"]; !got.Equal(time.Date(2024, 1, 2, 3, 4, 5, 678000000, time.UTC)) {
		t.Errorf("1.0.0 = %v", got)
	}

	if _, ok := dates["1.0.0-x86_64-linux"]; !ok {
		t.Errorf("platform version missing: %v", dates)
	}

	if got := dates["2.0.0"]; !got.Equal(time.Date(2024, 2, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("2.0.0 = %v, want the newest of the ABIs", got)
	}

	if got, ok := dates["3.0.0"]; !ok || !got.IsZero() {
		t.Errorf("3.0.0 = %v, %v, want listed with an unknown time", got, ok)
	}

	if d, err := parseGemDates([]byte(`[]`)); d != nil || err != nil {
		t.Errorf("empty = %v, %v", d, err)
	}
}
