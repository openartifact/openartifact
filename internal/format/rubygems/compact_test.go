// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/pkg/compactindex"
)

func compactGem(name, version, platform, sumDigit, deps string) *models.RubyGem {
	return &models.RubyGem{
		Name: name, Version: version, Platform: platform, ChecksumSHA256: strings.Repeat(sumDigit, 64),
		Dependencies: []byte(deps), CreatedAt: time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC),
	}
}

func compactFixture(t *testing.T) *HTTPHandler {
	t.Helper()

	h, db, _, _ := newTestHandler(t)
	db.gems = []*models.RubyGem{
		compactGem("demo", "1.0.0", "ruby", "a", `[{"name":"rake","requirement":">= 1.0, < 3","type":"runtime"},`+
			`{"name":"rspec","requirement":">= 0","type":"development"}]`),
		compactGem("demo", "2.0.0.beta1", "ruby", "b", `[]`),
		compactGem("demo", "1.0.0", "java", "c", `[{"name":"nokogiri","requirement":"","type":"runtime"}]`),
		compactGem("gone", "3.0.0", "ruby", "d", `[]`),
		compactGem("bad name", "1.0.0", "ruby", "e", `[]`),
	}
	db.gems[3].Yanked = true

	return h
}

func TestCompactInfoRendersRuntimeDependenciesAndChecksum(t *testing.T) {
	h := compactFixture(t)

	w := getPath(h, "info/demo")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	want := "---\n" +
		"1.0.0 rake:>= 1.0&< 3|checksum:" + strings.Repeat("a", 64) + "\n" +
		"1.0.0-java nokogiri:>= 0|checksum:" + strings.Repeat("c", 64) + "\n" +
		"2.0.0.beta1 |checksum:" + strings.Repeat("b", 64) + "\n"
	if w.Body.String() != want {
		t.Errorf("info/demo:\n%s\nwant:\n%s", w.Body.String(), want)
	}

	if _, err := compactindex.ParseInfoBytes(w.Body.Bytes()); err != nil {
		t.Errorf("info is not parsable: %v", err)
	}

	for _, name := range []string{"gone", "unknown", "bad%20name"} {
		if w := getPath(h, "info/"+name); w.Code != http.StatusNotFound {
			t.Errorf("info/%s = %d, want 404", name, w.Code)
		}
	}
}

func TestCompactHeadersMirrorTheRubygemsServer(t *testing.T) {
	h := compactFixture(t)

	w := getPath(h, "info/demo")
	body := w.Body.Bytes()

	if got, want := w.Header().Get("ETag"), `"`+compactindex.InfoMD5(body)+`"`; got != want {
		t.Errorf("ETag %q, want %q", got, want)
	}

	sum := sha256.Sum256(body)
	digest := `sha-256="` + base64.StdEncoding.EncodeToString(sum[:]) + `"`

	if w.Header().Get("Repr-Digest") != digest || w.Header().Get("Digest") != digest {
		t.Errorf("digest headers %v, want %s", w.Header(), digest)
	}

	if w.Header().Get("Content-Encoding") != "" {
		t.Errorf("compact index must be served identity-encoded, got %q", w.Header().Get("Content-Encoding"))
	}

	req := formattest.Request(http.MethodGet, "/repos/myrepo/info/demo", nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))

	if w := formattest.Do(h.RegisterRepoRoutes, req); w.Code != http.StatusNotModified {
		t.Errorf("conditional = %d, want 304", w.Code)
	}
}

func TestCompactVersionsAndNamesAgreeWithInfo(t *testing.T) {
	h := compactFixture(t)

	versions := getPath(h, "versions")
	if versions.Code != http.StatusOK {
		t.Fatalf("versions status %d", versions.Code)
	}

	v, err := compactindex.ParseVersions(strings.NewReader(versions.Body.String()))
	if err != nil {
		t.Fatalf("versions not parsable: %v", err)
	}

	if v.CreatedAt != "2024-03-04T05:06:07Z" || len(v.Entries) != 1 {
		t.Fatalf("versions = %+v", v)
	}

	e := v.Entries[0]
	if e.Name != "demo" || strings.Join(e.Versions, ",") != "1.0.0,1.0.0-java,2.0.0.beta1" {
		t.Errorf("entry = %+v", e)
	}

	if info := getPath(h, "info/demo"); e.InfoMD5 != compactindex.InfoMD5(info.Body.Bytes()) {
		t.Errorf("versions md5 %s does not match info file md5 %s", e.InfoMD5, compactindex.InfoMD5(info.Body.Bytes()))
	}

	if w := getPath(h, "names"); w.Body.String() != "---\ndemo\n" {
		t.Errorf("names = %q", w.Body.String())
	}
}
