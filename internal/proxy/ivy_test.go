// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const ivyJar = "org.example/lib/1.0/jars/lib.jar"

func sha1Hex(s string) string {
	h := sha1.Sum([]byte(s))

	return hex.EncodeToString(h[:])
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))

	return hex.EncodeToString(h[:])
}

// ivyUpstream serves files by upstream path (below /repo); a path mapped to
// "" answers 404, one mapped to "5xx" answers 503.
func ivyUpstream(files map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/repo/")]

		switch {
		case !ok:
			http.NotFound(w, r)
		case body == "5xx":
			http.Error(w, "down", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, body)
		}
	}
}

func newIvyFixture(t *testing.T, format string, files map[string]string) *fixture {
	t.Helper()

	f := newFixture(t, ivyUpstream(files))
	f.repo.Format = format
	f.repo.UpstreamURL = f.upstream.URL + "/repo"

	return f
}

func TestIvyProxy_VerifiedReleaseIsPinned(t *testing.T) {
	for _, format := range []string{"ivy", "sbt"} {
		for name, files := range map[string]map[string]string{
			"sha256": {ivyJar: "jar", ivyJar + ".sha256": sum("jar") + "\n"},
			"sha1":   {ivyJar: "jar", ivyJar + ".sha1": strings.ToUpper(sha1Hex("jar")) + "  lib.jar\n"},
			"md5":    {ivyJar: "jar", ivyJar + ".md5": md5Hex("jar")},
		} {
			t.Run(format+"/"+name, func(t *testing.T) {
				f := newIvyFixture(t, format, files)

				if w := f.do(http.MethodGet, ivyJar); w.Code != http.StatusOK || w.Body.String() != "jar" {
					t.Fatalf("status %d body %q", w.Code, w.Body.String())
				}

				e := f.db.entry("repo-1", ivyJar)
				if e == nil || e.ExpiresAt.Before(time.Now().Add(365*24*time.Hour)) {
					t.Errorf("verified release entry = %+v, want it pinned as immutable", e)
				}
			})
		}
	}
}

func TestIvyProxy_ChecksumMismatchIsRefusedAndNotCached(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"sha256": {ivyJar: "jar", ivyJar + ".sha256": sum("other")},
		"sha1":   {ivyJar: "jar", ivyJar + ".sha1": sha1Hex("other")},
		"md5":    {ivyJar: "jar", ivyJar + ".md5": md5Hex("other")},
		// The strongest published checksum decides, a matching weaker one does not save it.
		"sha256 wins over sha1": {ivyJar: "jar", ivyJar + ".sha256": sum("other"), ivyJar + ".sha1": sha1Hex("jar")},
	} {
		t.Run(name, func(t *testing.T) {
			f := newIvyFixture(t, "ivy", files)

			if w := f.do(http.MethodGet, ivyJar); w.Code != http.StatusBadGateway {
				t.Fatalf("status %d body %q, want 502", w.Code, w.Body.String())
			}

			if e := f.db.entry("repo-1", ivyJar); e != nil {
				t.Errorf("a file that failed its checksum was cached: %+v", e)
			}
		})
	}
}

func TestIvyProxy_NoChecksumIsServedButNotPinned(t *testing.T) {
	f := newIvyFixture(t, "ivy", map[string]string{
		ivyJar: "jar",
		// A sidecar with no digest in it publishes nothing to check against.
		ivyJar + ".sha1": "<html>not a checksum</html>",
	})

	if w := f.do(http.MethodGet, ivyJar); w.Code != http.StatusOK || w.Body.String() != "jar" {
		t.Fatalf("status %d body %q", w.Code, w.Body.String())
	}

	e := f.db.entry("repo-1", ivyJar)
	if e == nil || e.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Errorf("unverified entry = %+v, want the repository TTL (10m)", e)
	}
}

func TestIvyProxy_FailingChecksumRequestRefusesTheFile(t *testing.T) {
	f := newIvyFixture(t, "ivy", map[string]string{ivyJar: "jar", ivyJar + ".sha256": "5xx"})

	if w := f.do(http.MethodGet, ivyJar); w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502: a file must not be accepted unchecked because the checksum request failed", w.Code)
	}

	if e := f.db.entry("repo-1", ivyJar); e != nil {
		t.Errorf("cached %+v", e)
	}
}

func TestIvyProxy_MissingFileCostsOneUpstreamRequest(t *testing.T) {
	f := newIvyFixture(t, "ivy", map[string]string{})

	if w := f.do(http.MethodGet, ivyJar); w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}

	if got := f.hits.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1: checksums are only asked for after the file exists", got)
	}
}

func TestIvyProxy_ChecksumsAndMetadataAreMirroredAsTheyAre(t *testing.T) {
	files := map[string]string{
		ivyJar + ".sha1":                            sha1Hex("anything"),
		ivyJar + ".asc":                             "-----BEGIN PGP SIGNATURE-----",
		"com/example/lib/maven-metadata.xml":        "<metadata/>",
		"com/example/lib/maven-metadata.xml.sha1":   sha1Hex("<metadata/>"),
		"org.example/lib/1.0/ivys/ivy.xml.sha1":     sha1Hex("<ivy-module/>"),
		"org.example/lib/1.0-SNAPSHOT/jars/lib.jar": "snap",
	}

	for p, want := range files {
		t.Run(p, func(t *testing.T) {
			f := newIvyFixture(t, "sbt", files)

			if w := f.do(http.MethodGet, p); w.Code != http.StatusOK || w.Body.String() != want {
				t.Fatalf("status %d body %q, want %q", w.Code, w.Body.String(), want)
			}

			e := f.db.entry("repo-1", p)
			if e == nil || e.ExpiresAt.After(time.Now().Add(time.Hour)) {
				t.Errorf("entry = %+v, want the repository TTL: not a verified release", e)
			}
		})
	}
}

func TestIvyProxy_VerifiedSnapshotIsNotPinned(t *testing.T) {
	snap := "org.example/lib/1.0-SNAPSHOT/jars/lib.jar"
	f := newIvyFixture(t, "ivy", map[string]string{snap: "snap", snap + ".sha1": sha1Hex("snap")})

	if w := f.do(http.MethodGet, snap); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	if e := f.db.entry("repo-1", snap); e == nil || e.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Errorf("snapshot entry = %+v, want the repository TTL", e)
	}
}

func TestIvyProxy_HTMLForAJarIsRefused(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>captive portal</html>")
	})
	f.repo.Format = "ivy"

	if w := f.do(http.MethodGet, ivyJar); w.Code != http.StatusBadGateway {
		t.Errorf("status %d, want 502", w.Code)
	}
}

func TestIvyImmutable(t *testing.T) {
	tests := map[string]bool{
		ivyJar:                                              true,
		"com/example/lib/1.0/lib-1.0.pom":                   true,
		"org.example/lib/1.0/ivys/ivy.xml":                  true,
		"org.example/lib/1.0/jars/lib.jar.sha1":             false,
		"org.example/lib/1.0/jars/lib.jar.asc":              false,
		"com/example/lib/maven-metadata.xml":                false,
		"com/example/lib/1.0/lib-1.0.jar.md5":               false,
		"com/example/lib/1.0-SNAPSHOT/lib-1.0-SNAPSHOT.jar": false,
	}

	for key, want := range tests {
		for _, format := range []string{"ivy", "sbt"} {
			if got := Immutable(format, key); got != want {
				t.Errorf("Immutable(%q, %q) = %v, want %v", format, key, got, want)
			}
		}
	}
}

func TestParseIvyDigest(t *testing.T) {
	good := sha1Hex("x")
	tests := []struct {
		name string
		body string
		size int
		want string
		ok   bool
	}{
		{"bare", good, sha1.Size, good, true},
		{"newline", good + "\n", sha1.Size, good, true},
		{"uppercase", strings.ToUpper(good), sha1.Size, good, true},
		{"with file name", good + "  lib.jar\n", sha1.Size, good, true},
		{"wrong size", good, md5.Size, "", false},
		{"not hex", strings.Repeat("z", 2*sha1.Size), sha1.Size, "", false},
		{"html", "<html></html>", sha1.Size, "", false},
		{"empty", "", sha1.Size, "", false},
		{"blank", " \n", sha1.Size, "", false},
		{"oversized", good + strings.Repeat(" ", maxIvyDigestBytes), sha1.Size, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseIvyDigest([]byte(tt.body), tt.size)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseIvyDigest = %q, %v, want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func FuzzIvyParseDigest(f *testing.F) {
	for _, s := range []string{
		sha1Hex("x"), sha1Hex("x") + "  lib.jar\n", sum("x") + "\n", md5Hex("x"), "", " ", "\x00", "zz", "<html>",
	} {
		f.Add([]byte(s), sha1.Size)
	}

	f.Fuzz(func(t *testing.T, body []byte, size int) {
		got, ok := parseIvyDigest(body, size)
		if !ok {
			if got != "" {
				t.Fatalf("digest %q returned with ok=false", got)
			}

			return
		}

		raw, err := hex.DecodeString(got)
		if err != nil || len(raw) != size || got != strings.ToLower(got) {
			t.Fatalf("parseIvyDigest(%q, %d) = %q: not a lowercase hex digest of %d bytes", body, size, got, size)
		}
	})
}
