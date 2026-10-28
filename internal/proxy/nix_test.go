// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	pkgnix "github.com/openartifact/openartifact/pkg/nix"
)

const nixTestHash = "b1c2d3f4g5h6i7j8k9l0m1n2p3q4r5s6"

// nixCache is a binary cache upstream serving one store path.
type nixCache struct {
	mu      sync.Mutex
	narinfo string
	nar     []byte
	narPath string
}

func (c *nixCache) set(narinfo string, nar []byte, narPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.narinfo, c.nar, c.narPath = narinfo, nar, narPath
}

func (c *nixCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch r.URL.Path {
	case "/nix-cache-info":
		w.Header().Set("Content-Type", "text/x-nix-cache-info")
		_, _ = w.Write([]byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"))
	case "/" + nixTestHash + ".narinfo":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(c.narinfo))
	case "/" + c.narPath:
		_, _ = w.Write(c.nar)
	default:
		http.NotFound(w, r)
	}
}

// nixNarinfoFor renders the narinfo of a store path whose NAR file has the given
// bytes and lives at narPath (the file name need not be its hash).
func nixNarinfoFor(nar []byte, narPath string, mutate func(*pkgnix.NarInfo)) string {
	file := sha256.Sum256(nar)
	narHash := sha256.Sum256([]byte("uncompressed " + narPath))

	info := &pkgnix.NarInfo{
		StorePath:   pkgnix.StoreDir + "/" + nixTestHash + "-hello-2.12",
		URL:         narPath,
		Compression: "zstd",
		FileHash:    pkgnix.SHA256(file).String(),
		FileSize:    int64(len(nar)),
		NarHash:     pkgnix.SHA256(narHash).String(),
		NarSize:     4242,
		Sigs:        []string{"cache.example-1:AAAA", "other-2:BBBB"},
	}

	if mutate != nil {
		mutate(info)
	}

	return info.String()
}

func newNixFixture(t *testing.T, c *nixCache) *fixture {
	t.Helper()

	f := newFixture(t, c.ServeHTTP)
	f.repo.Format = "nix"
	f.repo.UpstreamURL = f.upstream.URL

	return f
}

// A zstd-style NAR: its name is the NarHash, not the sha256 of the file.
func nixZstdFixture(t *testing.T) (*fixture, *nixCache, []byte, string) {
	t.Helper()

	nar := []byte("compressed nar bytes")
	narPath := "nar/" + pkgnix.EncodeBase32(make([]byte, sha256.Size)) + ".nar.zst"
	c := &nixCache{}
	c.set(nixNarinfoFor(nar, narPath, nil), nar, narPath)

	return newNixFixture(t, c), c, nar, narPath
}

func TestNix_NarinfoCachedByTTLAndPassedThroughUntouched(t *testing.T) {
	f, c, _, _ := nixZstdFixture(t)
	key := nixTestHash + ".narinfo"

	first := f.do(http.MethodGet, key)
	if first.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", first.Code)
	}

	if ct := first.Header().Get("Content-Type"); ct != "text/x-nix-narinfo" {
		t.Errorf("Content-Type = %q", ct)
	}

	for _, sig := range []string{"Sig: cache.example-1:AAAA\n", "Sig: other-2:BBBB\n"} {
		if !strings.Contains(first.Body.String(), sig) {
			t.Errorf("signature line %q lost:\n%s", sig, first.Body)
		}
	}

	if body := f.do(http.MethodGet, key).Body.String(); body != first.Body.String() {
		t.Error("cached narinfo differs from the first answer")
	}

	if head := f.do(http.MethodHead, key); head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d body bytes", head.Code, head.Body.Len())
	}

	if got := f.hits.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1", got)
	}

	if ttl := time.Until(f.db.entry("repo-1", key).ExpiresAt); ttl > 600*time.Second || ttl < 500*time.Second {
		t.Errorf("narinfo expires in %v, want the repository TTL", ttl)
	}

	// After the TTL the narinfo is fetched again, and a changed upstream shows.
	c.set(strings.Replace(first.Body.String(), "other-2:BBBB", "other-2:CCCC", 1), nil, "")
	f.db.expire("repo-1", key)

	if body := f.do(http.MethodGet, key).Body.String(); !strings.Contains(body, "CCCC") {
		t.Errorf("expired narinfo not refetched:\n%s", body)
	}
}

func TestNix_AbsentNarinfoIsNegativelyCached(t *testing.T) {
	f, _, _, _ := nixZstdFixture(t)
	missing := strings.Repeat("0", 32) + ".narinfo"

	for range 3 {
		if w := f.do(http.MethodGet, missing); w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	}

	if got := f.hits.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1", got)
	}
}

func TestNix_CacheInfoPassesThroughTheCache(t *testing.T) {
	f, _, _, _ := nixZstdFixture(t)

	for range 2 {
		w := f.do(http.MethodGet, "nix-cache-info")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "StoreDir: /nix/store") {
			t.Fatalf("nix-cache-info = %d %q", w.Code, w.Body)
		}
	}

	if got := f.hits.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1", got)
	}
}

func TestNix_NarVerifiedAgainstNarinfoAndCachedImmutable(t *testing.T) {
	f, _, nar, narPath := nixZstdFixture(t)

	if w := f.do(http.MethodGet, nixTestHash+".narinfo"); w.Code != http.StatusOK {
		t.Fatalf("narinfo status = %d", w.Code)
	}

	for range 2 {
		w := f.do(http.MethodGet, narPath)
		if w.Code != http.StatusOK || w.Body.String() != string(nar) {
			t.Fatalf("NAR = %d %q", w.Code, w.Body)
		}
	}

	if got := f.hits.Load(); got != 2 {
		t.Errorf("upstream requests = %d, want 2 (narinfo, NAR)", got)
	}

	if ttl := time.Until(f.db.entry("repo-1", narPath).ExpiresAt); ttl < 50*365*24*time.Hour {
		t.Errorf("verified NAR expires in %v, want effectively never", ttl)
	}
}

func TestNix_TamperedNarIsRefusedAndNotCached(t *testing.T) {
	f, c, nar, narPath := nixZstdFixture(t)
	f.do(http.MethodGet, nixTestHash+".narinfo")

	c.set(nixNarinfoFor(nar, narPath, nil), []byte("tampered nar bytes!"), narPath)

	if w := f.do(http.MethodGet, narPath); w.Code != http.StatusBadGateway {
		t.Fatalf("tampered NAR status = %d, want 502", w.Code)
	}

	if e := f.db.entry("repo-1", narPath); e != nil {
		t.Errorf("tampered NAR was cached: %+v", e)
	}
}

func TestNix_NarWithWrongListedSizeIsRefused(t *testing.T) {
	f, c, nar, narPath := nixZstdFixture(t)
	c.set(nixNarinfoFor(nar, narPath, func(n *pkgnix.NarInfo) { n.FileSize++ }), nar, narPath)
	f.do(http.MethodGet, nixTestHash+".narinfo")

	if w := f.do(http.MethodGet, narPath); w.Code != http.StatusBadGateway {
		t.Fatalf("NAR with a wrong listed size: status = %d, want 502", w.Code)
	}
}

func TestNix_NarWithoutNarinfoIsRefusedUnlessNamedByItsHash(t *testing.T) {
	f, c, nar, _ := nixZstdFixture(t)
	c.set("", nar, "nar/plain-name.nar.zst")

	if w := f.do(http.MethodGet, "nar/plain-name.nar.zst"); w.Code != http.StatusBadGateway {
		t.Fatalf("NAR with no narinfo on record: status = %d, want 502", w.Code)
	}

	if f.hits.Load() != 0 {
		t.Error("a NAR nobody vouches for was requested from the upstream")
	}

	selfNamed := "nar/" + pkgnix.EncodeBase32(sha256Sum(nar)) + ".nar.xz"
	c.set("", nar, selfNamed)

	if w := f.do(http.MethodGet, selfNamed); w.Code != http.StatusOK || w.Body.String() != string(nar) {
		t.Fatalf("self-named NAR = %d %q", w.Code, w.Body)
	}

	c.set("", []byte("other bytes"), selfNamed)
	f.db.expire("repo-1", selfNamed)

	if w := f.do(http.MethodGet, selfNamed); w.Code == http.StatusOK && w.Body.String() != string(nar) {
		t.Error("a body that is not the named hash was served")
	}
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

func TestNix_SidecarKeyIsNotRequestable(t *testing.T) {
	f, _, _, narPath := nixZstdFixture(t)
	f.do(http.MethodGet, nixTestHash+".narinfo")

	if f.db.entry("repo-1", nixSidecarKey(narPath)) == nil {
		t.Fatal("no NAR expectation was recorded")
	}

	for _, p := range []string{nixSidecarKey(narPath), "nixnar%5C" + narPath, "nixnar/" + narPath} {
		before := f.hits.Load()

		if w := f.do(http.MethodGet, p); w.Code == http.StatusOK {
			t.Errorf("GET %q = 200", p)
		}

		if f.hits.Load() != before && strings.Contains(p, "\\") {
			t.Errorf("GET %q reached the upstream", p)
		}
	}
}

func TestNix_InvalidNarinfoIsRefusedAndNotCached(t *testing.T) {
	nar := []byte("nar")
	narPath := "nar/" + pkgnix.EncodeBase32(sha256Sum(nar)) + ".nar.xz"
	other := "c" + nixTestHash[1:]

	for name, body := range map[string]string{
		"garbage":      "this is not a narinfo",
		"other path":   strings.Replace(nixNarinfoFor(nar, narPath, nil), nixTestHash, other, 1),
		"url outside":  nixNarinfoFor(nar, narPath, func(n *pkgnix.NarInfo) { n.URL = "files/x.nar.xz" }),
		"url upwards":  nixNarinfoFor(nar, narPath, func(n *pkgnix.NarInfo) { n.URL = "nar/../x.nar.xz" }),
		"url absolute": nixNarinfoFor(nar, narPath, func(n *pkgnix.NarInfo) { n.URL = "https://evil.example/nar/x.nar" }),
	} {
		t.Run(name, func(t *testing.T) {
			c := &nixCache{}
			c.set(body, nar, narPath)
			f := newNixFixture(t, c)

			if w := f.do(http.MethodGet, nixTestHash+".narinfo"); w.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", w.Code)
			}

			if e := f.db.entry("repo-1", nixTestHash+".narinfo"); e != nil {
				t.Errorf("invalid narinfo was cached: %+v", e)
			}
		})
	}
}

func TestNix_UncompressedNarinfoWithoutFileHashUsesNarHash(t *testing.T) {
	nar := []byte("bare nar")
	narPath := "nar/" + pkgnix.EncodeBase32(make([]byte, sha256.Size)) + ".nar"
	body := nixNarinfoFor(nar, narPath, func(n *pkgnix.NarInfo) {
		n.Compression, n.FileHash, n.FileSize = "none", "", 0
		n.NarHash, n.NarSize = pkgnix.SHA256(sha256.Sum256(nar)).String(), int64(len(nar))
	})

	c := &nixCache{}
	c.set(body, nar, narPath)
	f := newNixFixture(t, c)
	f.do(http.MethodGet, nixTestHash+".narinfo")

	if w := f.do(http.MethodGet, narPath); w.Code != http.StatusOK || w.Body.String() != string(nar) {
		t.Fatalf("NAR = %d %q", w.Code, w.Body)
	}
}

func TestNix_OtherPathsPassThroughAndKeyKinds(t *testing.T) {
	for key, want := range map[string][2]bool{
		"nar/x.nar.xz":                  {false, true},
		nixTestHash + ".narinfo":        {true, false},
		nixTestHash + ".ls":             {true, false},
		"nix-cache-info":                {true, false},
		"log/" + nixTestHash + "-x.drv": {true, false},
	} {
		if got := isMetadataKey("nix", key); got != want[0] {
			t.Errorf("isMetadataKey(%q) = %v", key, got)
		}

		if got := Immutable("nix", key); got != want[1] {
			t.Errorf("Immutable(%q) = %v", key, got)
		}
	}
}

func TestNix_NarOverTheBodyLimitIsRefused(t *testing.T) {
	f, _, nar, narPath := nixZstdFixture(t)
	f.svc.maxBody = int64(len(nar) - 1)
	f.do(http.MethodGet, nixTestHash+".narinfo")

	if w := f.do(http.MethodGet, narPath); w.Code != http.StatusBadGateway {
		t.Fatalf("oversized NAR status = %d, want 502", w.Code)
	}
}
