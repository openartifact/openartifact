// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocipath"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	ociManifestType = "application/vnd.oci.image.manifest.v1+json"
	ociRealmService = "registry.test"
)

func digestOf(data string) string {
	h := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(h[:])
}

// fakeOCIRegistry is an upstream registry with bearer-token authentication.
type fakeOCIRegistry struct {
	srv *httptest.Server

	mu           sync.Mutex
	manifests    map[string]string // "<name>:<ref>" -> body
	digestHeader map[string]string // "<name>:<ref>" -> Docker-Content-Digest override
	blobs        map[string]string // "<name>@<digest>" -> body served for it
	tags         map[string][]string
	token        string
	requests     []string // "METHOD path"
	tokenQueries []url.Values
	tokenBasic   []string // "user:pass" seen at the token endpoint
	status       int      // when non-zero, every /v2 request answers it
	deny         bool     // authenticated requests are answered 401
	realm        string   // token endpoint named by the challenge; the registry itself when empty
	manifestCT   string   // Content-Type of manifest answers; ociManifestType when empty
	referrersCT  string   // Content-Type of the referrers answer
}

func newFakeOCIRegistry(t *testing.T) *fakeOCIRegistry {
	t.Helper()
	t.Setenv(allowPrivateEnv, "true")

	reg := &fakeOCIRegistry{
		manifests:    map[string]string{},
		digestHeader: map[string]string{},
		blobs:        map[string]string{},
		tags:         map[string][]string{},
		token:        "tok-1",
	}

	reg.srv = httptest.NewServer(http.HandlerFunc(reg.handle))
	t.Cleanup(reg.srv.Close)

	return reg
}

func (g *fakeOCIRegistry) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if r.URL.Path == "/token" {
		g.tokenQueries = append(g.tokenQueries, r.URL.Query())

		if u, p, ok := r.BasicAuth(); ok {
			g.tokenBasic = append(g.tokenBasic, u+":"+p)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"token": g.token, "expires_in": 300})

		return
	}

	// Only requests that carry the right token (or hit an injected failure)
	// count: the anonymous probe that provokes the challenge does not.
	if g.status != 0 || r.Header.Get("Authorization") == "Bearer "+g.token {
		g.requests = append(g.requests, r.Method+" "+r.URL.Path)
	}

	if g.status != 0 {
		w.WriteHeader(g.status)
		return
	}

	if r.Header.Get("Authorization") != "Bearer "+g.token || g.deny {
		if r.Header.Get("Authorization") == "Bearer "+g.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		req := ocipath.Parse(strings.TrimPrefix(r.URL.Path, "/v2/"))

		realm := g.srv.URL
		if g.realm != "" {
			realm = g.realm
		}

		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="%s",scope="repository:%s:pull"`, realm, ociRealmService, req.Name))
		w.WriteHeader(http.StatusUnauthorized)

		return
	}

	g.serveV2(w, r)
}

func (g *fakeOCIRegistry) serveV2(w http.ResponseWriter, r *http.Request) {
	req := ocipath.Parse(strings.TrimPrefix(r.URL.Path, "/v2/"))

	switch req.Kind {
	case ocipath.KindManifest:
		k := req.Name + ":" + req.Ref

		body, ok := g.manifests[k]
		if !ok {
			http.NotFound(w, r)
			return
		}

		dg := digestOf(body)
		if o, has := g.digestHeader[k]; has {
			dg = o
		}

		ct := ociManifestType
		if g.manifestCT != "" {
			ct = g.manifestCT
		}

		w.Header().Set("Content-Type", ct)
		w.Header().Set("Docker-Content-Digest", dg)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))

		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(body))
		}
	case ocipath.KindBlob:
		body, ok := g.blobs[req.Name+"@"+req.Ref]
		if !ok {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	case ocipath.KindTags:
		tags := g.tags[req.Name]
		if tags == nil {
			http.NotFound(w, r)
			return
		}

		if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n < len(tags) {
			w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?last=%s&n=%d>; rel="next"`, req.Name, tags[n-1], n))
			tags = tags[:n]
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"name": req.Name, "tags": tags})
	case ocipath.KindReferrers:
		w.Header().Set("Content-Type", g.referrersCT)
		_, _ = io.WriteString(w, `{"schemaVersion":2,"manifests":[]}`)
	case ocipath.KindUnknown, ocipath.KindUploadStart, ocipath.KindUpload:
		http.NotFound(w, r)
	}
}

func (g *fakeOCIRegistry) addManifest(name, tag, body string) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.manifests[name+":"+tag] = body
	g.manifests[name+":"+digestOf(body)] = body

	return digestOf(body)
}

func (g *fakeOCIRegistry) addBlob(name, body string) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.blobs[name+"@"+digestOf(body)] = body

	return digestOf(body)
}

func (g *fakeOCIRegistry) count(prefix string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	n := 0

	for _, r := range g.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}

	return n
}

func (g *fakeOCIRegistry) set(fn func()) {
	g.mu.Lock()
	defer g.mu.Unlock()

	fn()
}

type ociFixture struct {
	t    *testing.T
	reg  *fakeOCIRegistry
	svc  *Service
	db   *memDB
	repo *models.Repository
}

func newOCIFixture(t *testing.T) *ociFixture {
	t.Helper()

	f := &ociFixture{t: t, reg: newFakeOCIRegistry(t), db: newMemDB()}

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}

	f.svc = NewService(log, f.db, store)
	f.repo = &models.Repository{
		ID:                      "docker-1",
		Name:                    "hub",
		Format:                  "docker",
		RepoType:                "proxy",
		UpstreamURL:             f.reg.srv.URL,
		CacheTTLSeconds:         600,
		NegativeCacheTTLSeconds: 60,
		MaxStaleSeconds:         3600,
	}

	return f
}

// do issues a request for /v2/<path> against the proxy repository.
func (f *ociFixture) do(method, path string) *httptest.ResponseRecorder {
	f.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, "/v2/"+path, nil)
	w := httptest.NewRecorder()
	f.svc.ServeOCI(w, req, f.repo, ocipath.Parse(path))

	return w
}

func ociErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
		t.Fatalf("not a registry error response (%d): %q", w.Code, w.Body.String())
	}

	return body.Errors[0].Code
}

const alpineManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`

func TestOCI_PullAnswersTokenChallengeAndCaches(t *testing.T) {
	f := newOCIFixture(t)
	dg := f.reg.addManifest("library/alpine", "3.20", alpineManifest)
	layer := f.reg.addBlob("library/alpine", "layer-bytes")

	m := f.do(http.MethodGet, "hub/library/alpine/manifests/3.20")
	if m.Code != http.StatusOK || m.Body.String() != alpineManifest {
		t.Fatalf("manifest: %d %q", m.Code, m.Body.String())
	}

	if got := m.Header().Get("Content-Type"); got != ociManifestType {
		t.Errorf("Content-Type = %q, want the upstream media type", got)
	}

	if got := m.Header().Get("Docker-Content-Digest"); got != dg {
		t.Errorf("Docker-Content-Digest = %q, want %q", got, dg)
	}

	if w := f.do(http.MethodGet, "hub/library/alpine/manifests/"+dg); w.Code != http.StatusOK {
		t.Fatalf("manifest by digest: %d", w.Code)
	}

	b := f.do(http.MethodGet, "hub/library/alpine/blobs/"+layer)
	if b.Code != http.StatusOK || b.Body.String() != "layer-bytes" || b.Header().Get("Docker-Content-Digest") != layer {
		t.Fatalf("blob: %d %q digest %q", b.Code, b.Body.String(), b.Header().Get("Docker-Content-Digest"))
	}

	if len(f.reg.tokenQueries) != 1 {
		t.Fatalf("token requests = %d, want 1 (the token is reused for the same image)", len(f.reg.tokenQueries))
	}

	q := f.reg.tokenQueries[0]
	if q.Get("service") != ociRealmService || q.Get("scope") != "repository:library/alpine:pull" {
		t.Errorf("token request = %v, want the challenge's service and scope", q)
	}

	if len(f.reg.tokenBasic) != 0 {
		t.Errorf("anonymous proxy sent credentials to the token endpoint: %v", f.reg.tokenBasic)
	}

	before := len(f.reg.requests)

	// Second pull of everything: no upstream traffic at all.
	f.reg.srv.Close()

	if w := f.do(http.MethodGet, "hub/library/alpine/manifests/3.20"); w.Code != http.StatusOK || w.Body.String() != alpineManifest {
		t.Errorf("cached manifest: %d %q", w.Code, w.Body.String())
	}

	if w := f.do(http.MethodGet, "hub/library/alpine/manifests/"+dg); w.Code != http.StatusOK || w.Header().Get("Docker-Content-Digest") != dg {
		t.Errorf("cached manifest by digest: %d", w.Code)
	}

	if w := f.do(http.MethodGet, "hub/library/alpine/blobs/"+layer); w.Code != http.StatusOK || w.Body.String() != "layer-bytes" {
		t.Errorf("cached blob with upstream unreachable: %d %q", w.Code, w.Body.String())
	}

	if len(f.reg.requests) != before {
		t.Errorf("upstream saw %d request(s) for cached content", len(f.reg.requests)-before)
	}
}

// Without a digest TTL (workers.proxyDockerDigestTtl = 0) digest-addressed
// entries are open-ended: only an admin purge removes them.
func TestOCI_DigestContentIsOpenEndedWithoutDigestTTL(t *testing.T) {
	f := newOCIFixture(t)
	dg := f.reg.addManifest("library/alpine", "3.20", alpineManifest)
	layer := f.reg.addBlob("library/alpine", "layer-bytes")

	f.do(http.MethodGet, "hub/library/alpine/manifests/3.20")
	f.do(http.MethodGet, "hub/library/alpine/manifests/"+dg)
	f.do(http.MethodGet, "hub/library/alpine/blobs/"+layer)

	tests := map[string]bool{
		"library/alpine/manifests/3.20":  false,
		"library/alpine/manifests/" + dg: true,
		"library/alpine/blobs/" + layer:  true,
	}

	for key, immutable := range tests {
		e := f.db.entry("docker-1", key)
		if e == nil {
			t.Fatalf("%s not cached", key)
		}

		if got := time.Until(e.ExpiresAt) > 24*time.Hour; got != immutable {
			t.Errorf("%s: long-lived = %v, want %v (expires %v)", key, got, immutable, e.ExpiresAt)
		}
	}
}

func TestOCI_ExpiredTokenIsRenewed(t *testing.T) {
	f := newOCIFixture(t)
	f.reg.addManifest("library/alpine", "3.20", alpineManifest)
	f.reg.addManifest("library/alpine", "3.19", alpineManifest+" ")

	if w := f.do(http.MethodGet, "hub/library/alpine/manifests/3.20"); w.Code != http.StatusOK {
		t.Fatalf("first pull: %d", w.Code)
	}

	f.reg.set(func() { f.reg.token = "tok-2" }) // the upstream no longer accepts the cached token

	if w := f.do(http.MethodGet, "hub/library/alpine/manifests/3.19"); w.Code != http.StatusOK {
		t.Fatalf("pull with a rotated token: %d %q", w.Code, w.Body.String())
	}

	if len(f.reg.tokenQueries) != 2 {
		t.Errorf("token requests = %d, want 2", len(f.reg.tokenQueries))
	}
}

func TestOCI_StoredCredentialsAuthenticateTheTokenRequest(t *testing.T) {
	f := newOCIFixture(t)
	f.repo.UpstreamUsername = "robot"
	f.repo.UpstreamPasswordEnc = "s3cret" // legacy plaintext values decrypt to themselves
	f.reg.addManifest("team/app", "1", alpineManifest)

	if w := f.do(http.MethodGet, "hub/team/app/manifests/1"); w.Code != http.StatusOK {
		t.Fatalf("pull: %d %q", w.Code, w.Body.String())
	}

	if len(f.reg.tokenBasic) != 1 || f.reg.tokenBasic[0] != "robot:s3cret" {
		t.Errorf("token endpoint credentials = %v, want robot:s3cret", f.reg.tokenBasic)
	}
}

// A token realm on another origin is asked anonymously: the stored credentials
// belong to the upstream, and the realm is named by the upstream's response.
func TestOCI_ForeignTokenRealmGetsNoCredentials(t *testing.T) {
	f := newOCIFixture(t)
	f.repo.UpstreamUsername = "robot"
	f.repo.UpstreamPasswordEnc = "s3cret"
	f.reg.addManifest("team/app", "1", alpineManifest)

	var (
		mu   sync.Mutex
		seen []string
	)

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		u, p, ok := r.BasicAuth()
		seen = append(seen, fmt.Sprintf("%v:%s:%s", ok, u, p))

		_ = json.NewEncoder(w).Encode(map[string]any{"token": "tok-1", "expires_in": 300})
	}))
	t.Cleanup(other.Close)

	f.reg.set(func() { f.reg.realm = other.URL })

	if w := f.do(http.MethodGet, "hub/team/app/manifests/1"); w.Code != http.StatusOK {
		t.Fatalf("pull: %d %q", w.Code, w.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()

	if len(seen) != 1 || seen[0] != "false::" {
		t.Errorf("foreign token endpoint saw %v, want one anonymous request", seen)
	}
}

func TestRealmMayReceiveCredentials(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		realm string
		want  bool
	}{
		{"same origin http", "http://127.0.0.1:5000", "http://127.0.0.1:5000/token", true},
		{"same origin https default port", "https://reg.example.com", "https://reg.example.com:443/token", true},
		{"other port same host http", "http://127.0.0.1:5000", "http://127.0.0.1:6000/token", false},
		{"docker hub auth host", "https://registry-1.docker.io", "https://auth.docker.io/token", true},
		{"same site plain http realm", "https://registry-1.docker.io", "http://auth.docker.io/token", false},
		{"other site", "https://registry-1.docker.io", "https://auth.evil.example/token", false},
		{"suffix lookalike", "https://registry-1.docker.io", "https://auth.docker.io.evil.example/token", false},
		{"prefix lookalike", "https://registry-1.docker.io", "https://evildocker.io/token", false},
		{"public suffix siblings", "https://a.github.io", "https://b.github.io/token", false},
		{"different ip literals", "https://10.0.0.1", "https://20.0.0.1/token", false},
		{"bare public suffix", "https://com", "https://org/token", false},
		{"plain http upstream https sibling", "http://reg.example.com", "https://auth.example.com/token", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ru, err := url.Parse(tc.realm)
			if err != nil {
				t.Fatal(err)
			}

			if got := realmMayReceiveCredentials(ociUpstream{base: tc.base}, ru); got != tc.want {
				t.Errorf("realmMayReceiveCredentials(%q, %q) = %v, want %v", tc.base, tc.realm, got, tc.want)
			}
		})
	}
}

// A token fetched with an old password is not reused after the password changes.
func TestOCI_TokenCacheKeyIncludesPassword(t *testing.T) {
	f := newOCIFixture(t)
	f.repo.UpstreamUsername = "robot"
	f.repo.UpstreamPasswordEnc = "old-pass"
	f.reg.addManifest("team/app", "1", alpineManifest)
	f.reg.addManifest("team/app", "2", alpineManifest+" ")

	if w := f.do(http.MethodGet, "hub/team/app/manifests/1"); w.Code != http.StatusOK {
		t.Fatalf("pull 1: %d %q", w.Code, w.Body.String())
	}

	f.repo.UpstreamPasswordEnc = "new-pass"

	if w := f.do(http.MethodGet, "hub/team/app/manifests/2"); w.Code != http.StatusOK {
		t.Fatalf("pull 2: %d %q", w.Code, w.Body.String())
	}

	want := []string{"robot:old-pass", "robot:new-pass"}
	if len(f.reg.tokenBasic) != 2 || f.reg.tokenBasic[0] != want[0] || f.reg.tokenBasic[1] != want[1] {
		t.Errorf("token endpoint credentials = %v, want %v", f.reg.tokenBasic, want)
	}
}

func TestOCI_BasicChallengeUsesStoredCredentials(t *testing.T) {
	t.Setenv(allowPrivateEnv, "true")

	var got []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		got = append(got, fmt.Sprintf("%v:%s:%s", ok, u, p))

		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		w.Header().Set("Content-Type", ociManifestType)
		_, _ = w.Write([]byte(alpineManifest))
	}))
	t.Cleanup(srv.Close)

	f := newOCIFixture(t)
	f.repo.UpstreamURL = srv.URL
	f.repo.UpstreamUsername = "u"
	f.repo.UpstreamPasswordEnc = "p"

	if w := f.do(http.MethodGet, "hub/team/app/manifests/1"); w.Code != http.StatusOK {
		t.Fatalf("pull: %d %q", w.Code, w.Body.String())
	}

	if len(got) != 2 || got[0] != "false::" || got[1] != "true:u:p" {
		t.Errorf("upstream saw %v, want an anonymous try then credentials", got)
	}
}

func TestOCI_DockerHubSingleSegmentGetsLibraryPrefix(t *testing.T) {
	tests := []struct {
		upstream, image, want string
	}{
		{"https://registry-1.docker.io", "alpine", "library/alpine"},
		{"https://registry-1.docker.io/", "library/alpine", "library/alpine"},
		{"https://registry-1.docker.io", "grafana/grafana", "grafana/grafana"},
		{"https://registry.example.com", "alpine", "alpine"},
		{"https://registry.example.com/ignored/path", "team/app", "team/app"},
	}

	for _, tt := range tests {
		got, err := ociTarget(&models.Repository{UpstreamURL: tt.upstream}, tt.image)
		if err != nil || got.name != tt.want {
			t.Errorf("ociTarget(%q, %q) = %q, %v; want %q", tt.upstream, tt.image, got.name, err, tt.want)
		}
	}
}

func TestOCI_SingleSegmentImageOnOtherRegistryIsNotPrefixed(t *testing.T) {
	f := newOCIFixture(t)
	f.reg.addManifest("alpine", "3.20", alpineManifest)

	if w := f.do(http.MethodGet, "hub/alpine/manifests/3.20"); w.Code != http.StatusOK {
		t.Fatalf("pull: %d %q", w.Code, w.Body.String())
	}
}

func TestOCI_DigestVerificationFailuresCacheNothing(t *testing.T) {
	f := newOCIFixture(t)

	other := digestOf("something else")

	f.reg.set(func() {
		// The registry serves other bytes than the digest names.
		f.reg.blobs["img@"+other] = "tampered"
		f.reg.manifests["img:"+other] = alpineManifest
		// A tag whose Docker-Content-Digest header disagrees with its body.
		f.reg.manifests["img:bad"] = alpineManifest
		f.reg.digestHeader["img:bad"] = other
	})

	tests := []struct {
		name, path, key string
	}{
		{"blob", "hub/img/blobs/" + other, "img/blobs/" + other},
		{"manifest by digest", "hub/img/manifests/" + other, "img/manifests/" + other},
		{"tag with wrong digest header", "hub/img/manifests/bad", "img/manifests/bad"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := f.do(http.MethodGet, tt.path)
			if w.Code != http.StatusBadGateway || ociErrorCode(t, w) != "UNAVAILABLE" {
				t.Errorf("status = %d, want 502 UNAVAILABLE", w.Code)
			}

			if e := f.db.entry("docker-1", tt.key); e != nil {
				t.Errorf("a body that failed verification was cached: %+v", e)
			}
		})
	}

	if len(f.db.blobs) != 0 {
		t.Errorf("blob rows recorded for unverified content: %v", f.db.blobs)
	}
}

func TestOCI_TagRevalidatedByHeadWhenDigestUnchanged(t *testing.T) {
	f := newOCIFixture(t)
	f.reg.addManifest("library/alpine", "3.20", alpineManifest)

	f.do(http.MethodGet, "hub/library/alpine/manifests/3.20")
	f.db.expire("docker-1", "library/alpine/manifests/3.20")

	w := f.do(http.MethodGet, "hub/library/alpine/manifests/3.20")
	if w.Code != http.StatusOK || w.Body.String() != alpineManifest {
		t.Fatalf("revalidated pull: %d %q", w.Code, w.Body.String())
	}

	if got := f.reg.count("GET /v2/library/alpine/manifests/3.20"); got != 1 {
		t.Errorf("manifest GETs = %d, want 1 (revalidation must not transfer the body)", got)
	}

	if got := f.reg.count("HEAD /v2/library/alpine/manifests/3.20"); got != 1 {
		t.Errorf("manifest HEADs = %d, want 1", got)
	}

	if e := f.db.entry("docker-1", "library/alpine/manifests/3.20"); e == nil || !e.ExpiresAt.After(time.Now()) {
		t.Errorf("TTL not renewed after revalidation: %+v", e)
	}

	// Fresh again: no further upstream traffic.
	f.do(http.MethodGet, "hub/library/alpine/manifests/3.20")

	if got := f.reg.count("HEAD "); got != 1 {
		t.Errorf("HEADs after a fresh hit = %d, want still 1", got)
	}
}

func TestOCI_MovedTagIsRefetched(t *testing.T) {
	f := newOCIFixture(t)
	f.reg.addManifest("library/alpine", "latest", alpineManifest)

	f.do(http.MethodGet, "hub/library/alpine/manifests/latest")

	moved := alpineManifest + "\n"
	newDigest := f.reg.addManifest("library/alpine", "latest", moved)

	f.db.expire("docker-1", "library/alpine/manifests/latest")

	w := f.do(http.MethodGet, "hub/library/alpine/manifests/latest")
	if w.Code != http.StatusOK || w.Body.String() != moved || w.Header().Get("Docker-Content-Digest") != newDigest {
		t.Fatalf("moved tag: %d digest %q body %q", w.Code, w.Header().Get("Docker-Content-Digest"), w.Body.String())
	}
}

func TestOCI_DeletedUpstreamTagIsNotServedStale(t *testing.T) {
	f := newOCIFixture(t)
	f.reg.addManifest("library/alpine", "gone", alpineManifest)

	f.do(http.MethodGet, "hub/library/alpine/manifests/gone")
	f.reg.set(func() { delete(f.reg.manifests, "library/alpine:gone") })
	f.db.expire("docker-1", "library/alpine/manifests/gone")

	w := f.do(http.MethodGet, "hub/library/alpine/manifests/gone")
	if w.Code != http.StatusNotFound || ociErrorCode(t, w) != "MANIFEST_UNKNOWN" {
		t.Errorf("= %d, want 404 MANIFEST_UNKNOWN", w.Code)
	}
}

func TestOCI_StaleTagServedWhenUpstreamFails(t *testing.T) {
	failures := map[string]func(*ociFixture){
		"upstream 503": func(f *ociFixture) { f.reg.set(func() { f.reg.status = http.StatusServiceUnavailable }) },
		"rate limited": func(f *ociFixture) { f.reg.set(func() { f.reg.status = http.StatusTooManyRequests }) },
		"unreachable":  func(f *ociFixture) { f.reg.srv.Close() },
	}

	for name, fail := range failures {
		t.Run(name, func(t *testing.T) {
			f := newOCIFixture(t)
			f.reg.addManifest("library/alpine", "3.20", alpineManifest)
			f.do(http.MethodGet, "hub/library/alpine/manifests/3.20")

			f.db.expire("docker-1", "library/alpine/manifests/3.20")
			fail(f)

			w := f.do(http.MethodGet, "hub/library/alpine/manifests/3.20")
			if w.Code != http.StatusOK || w.Body.String() != alpineManifest {
				t.Fatalf("stale pull: %d %q", w.Code, w.Body.String())
			}

			if w.Header().Get("Warning") != staleWarning {
				t.Errorf("Warning = %q, want the stale marker", w.Header().Get("Warning"))
			}

			if w.Header().Get("Docker-Content-Digest") != digestOf(alpineManifest) {
				t.Errorf("stale response lost its digest: %q", w.Header().Get("Docker-Content-Digest"))
			}
		})
	}
}

func TestOCI_TagPastMaxStaleFailsWhenUpstreamIsDown(t *testing.T) {
	f := newOCIFixture(t)
	f.reg.addManifest("library/alpine", "3.20", alpineManifest)
	f.do(http.MethodGet, "hub/library/alpine/manifests/3.20")

	f.db.expireAgo("docker-1", "library/alpine/manifests/3.20", 2*time.Hour) // max stale is 1h
	f.reg.set(func() { f.reg.status = http.StatusBadGateway })

	if w := f.do(http.MethodGet, "hub/library/alpine/manifests/3.20"); w.Code != http.StatusBadGateway || ociErrorCode(t, w) != "UNAVAILABLE" {
		t.Errorf("= %d, want 502 UNAVAILABLE", w.Code)
	}
}

func TestOCI_UpstreamNotFoundIsNegativelyCached(t *testing.T) {
	f := newOCIFixture(t)

	for range 2 {
		w := f.do(http.MethodGet, "hub/library/alpine/manifests/nope")
		if w.Code != http.StatusNotFound || ociErrorCode(t, w) != "MANIFEST_UNKNOWN" {
			t.Fatalf("= %d %q", w.Code, w.Body.String())
		}
	}

	if got := f.reg.count("GET /v2/library/alpine/manifests/nope"); got != 1 {
		t.Errorf("upstream GETs = %d, want 1", got)
	}

	w := f.do(http.MethodGet, "hub/library/alpine/blobs/"+digestOf("missing"))
	if w.Code != http.StatusNotFound || ociErrorCode(t, w) != "BLOB_UNKNOWN" {
		t.Errorf("missing blob = %d %q", w.Code, w.Body.String())
	}
}

func TestOCI_UpstreamDenialIsNotFoundAndNotCached(t *testing.T) {
	f := newOCIFixture(t)
	f.reg.addManifest("private/app", "1", alpineManifest)
	f.reg.set(func() { f.reg.deny = true })

	w := f.do(http.MethodGet, "hub/private/app/manifests/1")
	if w.Code != http.StatusNotFound || ociErrorCode(t, w) != "MANIFEST_UNKNOWN" {
		t.Errorf("= %d %q, want 404 MANIFEST_UNKNOWN", w.Code, w.Body.String())
	}

	if e := f.db.entry("docker-1", "private/app/manifests/1"); e != nil {
		t.Errorf("a denial was cached: %+v (fixing credentials would not take effect)", e)
	}
}

func TestOCI_PushIsDenied(t *testing.T) {
	f := newOCIFixture(t)

	tests := []struct {
		method, path string
		want         int
		code         string
	}{
		{http.MethodPost, "hub/team/app/blobs/uploads/", http.StatusMethodNotAllowed, "UNSUPPORTED"},
		{http.MethodPatch, "hub/team/app/blobs/uploads/uuid-1", http.StatusMethodNotAllowed, "UNSUPPORTED"},
		{http.MethodPut, "hub/team/app/blobs/uploads/uuid-1", http.StatusMethodNotAllowed, "UNSUPPORTED"},
		{http.MethodPut, "hub/team/app/manifests/1", http.StatusMethodNotAllowed, "UNSUPPORTED"},
		{http.MethodDelete, "hub/team/app/manifests/1", http.StatusMethodNotAllowed, "UNSUPPORTED"},
		{http.MethodDelete, "hub/team/app/blobs/" + digestOf("x"), http.StatusMethodNotAllowed, "UNSUPPORTED"},
		{http.MethodGet, "hub/team/app/blobs/uploads/uuid-1", http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN"},
	}

	for _, tt := range tests {
		w := f.do(tt.method, tt.path)
		if w.Code != tt.want || ociErrorCode(t, w) != tt.code {
			t.Errorf("%s %s = %d, want %d %s", tt.method, tt.path, w.Code, tt.want, tt.code)
		}

		if tt.want == http.StatusMethodNotAllowed && w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s %s: Allow = %q", tt.method, tt.path, w.Header().Get("Allow"))
		}
	}

	if n := len(f.reg.requests); n != 0 {
		t.Errorf("a refused write reached the upstream (%d requests)", n)
	}
}

func TestOCI_BlockedRepositoryIsDenied(t *testing.T) {
	f := newOCIFixture(t)
	f.repo.Blocked = true

	if w := f.do(http.MethodGet, "hub/team/app/manifests/1"); w.Code != http.StatusForbidden || ociErrorCode(t, w) != "DENIED" {
		t.Errorf("= %d", w.Code)
	}
}

func TestOCI_ImageNameIsRequiredAndValidated(t *testing.T) {
	f := newOCIFixture(t)

	if w := f.do(http.MethodGet, "hub/manifests/1"); w.Code != http.StatusNotFound {
		t.Errorf("no image: %d, want 404", w.Code)
	}

	if w := f.do(http.MethodGet, "hub/UPPER/case/manifests/1"); w.Code != http.StatusBadRequest || ociErrorCode(t, w) != "NAME_INVALID" {
		t.Errorf("invalid image name: %d", w.Code)
	}

	if w := f.do(http.MethodGet, "hub/team/app/blobs/sha256:short"); w.Code != http.StatusBadRequest || ociErrorCode(t, w) != "DIGEST_INVALID" {
		t.Errorf("invalid digest: %d", w.Code)
	}

	if n := len(f.reg.requests); n != 0 {
		t.Errorf("invalid requests reached the upstream (%d)", n)
	}
}

func TestOCI_TagListIsPassedThroughWithOwnName(t *testing.T) {
	f := newOCIFixture(t)
	f.reg.set(func() { f.reg.tags["alpine"] = []string{"3.18", "3.19", "3.20"} })

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v2/hub/alpine/tags/list?n=2", nil)
	w := httptest.NewRecorder()
	f.svc.ServeOCI(w, req, f.repo, ocipath.Parse("hub/alpine/tags/list"))

	var body struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
		t.Fatalf("= %d %q", w.Code, w.Body.String())
	}

	if body.Name != "hub/alpine" || strings.Join(body.Tags, ",") != "3.18,3.19" {
		t.Errorf("body = %+v, want the first page under the client's name", body)
	}

	if got := w.Header().Get("Link"); got != `</v2/hub/alpine/tags/list?last=3.19&n=2>; rel="next"` {
		t.Errorf("Link = %q, want the upstream's next page rewritten to the client's name", got)
	}

	if w := f.do(http.MethodGet, "hub/ghost/tags/list"); w.Code != http.StatusNotFound || ociErrorCode(t, w) != "NAME_UNKNOWN" {
		t.Errorf("unknown image tags = %d", w.Code)
	}
}

func TestParseChallenge(t *testing.T) {
	tests := []struct {
		in     string
		scheme string
		params map[string]string
	}{
		{
			`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull"`,
			"bearer",
			map[string]string{"realm": "https://auth.docker.io/token", "service": "registry.docker.io", "scope": "repository:library/alpine:pull"},
		},
		{
			`Bearer realm="https://x/token", service="a b", scope="repository:a:pull,push"`,
			"bearer",
			map[string]string{"realm": "https://x/token", "service": "a b", "scope": "repository:a:pull,push"},
		},
		{`Basic realm="Registry Realm"`, "basic", map[string]string{"realm": "Registry Realm"}},
		{`bearer realm=plain,service=s`, "bearer", map[string]string{"realm": "plain", "service": "s"}},
		{`Bearer realm="a\"b"`, "bearer", map[string]string{"realm": `a"b`}},
	}

	for _, tt := range tests {
		scheme, params := parseChallenge(tt.in)
		if scheme != tt.scheme || len(params) != len(tt.params) {
			t.Errorf("parseChallenge(%q) = %q %v", tt.in, scheme, params)
			continue
		}

		for k, v := range tt.params {
			if params[k] != v {
				t.Errorf("parseChallenge(%q)[%s] = %q, want %q", tt.in, k, params[k], v)
			}
		}
	}
}

// The type a manifest or listing is served under must be a registry JSON type:
// /v2/ responses come from the application origin, so an upstream must not be
// able to choose text/html (or smuggle one in the manifest body).
func TestOCI_UpstreamChosenContentTypeIsNeverEchoed(t *testing.T) {
	const htmlManifest = `{"schemaVersion":2,"mediaType":"text/html","note":"<script>alert(1)</script>"}`

	tests := []struct {
		name   string
		header string
		body   string
		want   string
	}{
		{"html header, html mediaType", "text/html", htmlManifest, "application/vnd.docker.distribution.manifest.v2+json"},
		{"svg header", "image/svg+xml", alpineManifest, ociManifestType},
		{"html header with body type", "text/html", `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json"}`, "application/vnd.docker.distribution.manifest.v2+json"},
		{"registry header kept", "application/vnd.docker.distribution.manifest.v2+json; charset=utf-8", alpineManifest, "application/vnd.docker.distribution.manifest.v2+json"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newOCIFixture(t)
			f.reg.manifestCT = tc.header
			f.reg.addManifest("library/alpine", "3.20", tc.body)

			for range 2 { // miss, then cache hit
				w := f.do(http.MethodGet, "hub/library/alpine/manifests/3.20")
				if w.Code != http.StatusOK {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}

				if got := w.Header().Get("Content-Type"); got != tc.want {
					t.Errorf("Content-Type = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestOCI_ReferrersContentTypeIsRestricted(t *testing.T) {
	dg := digestOf("subject")

	for in, want := range map[string]string{
		"text/html": "application/vnd.oci.image.index.v1+json",
		"":          "application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.index.v1+json": "application/vnd.oci.image.index.v1+json",
		"application/json":                        "application/json",
	} {
		f := newOCIFixture(t)
		f.reg.referrersCT = in

		w := f.do(http.MethodGet, "hub/library/alpine/referrers/"+dg)
		if w.Code != http.StatusOK {
			t.Fatalf("%q: status %d: %s", in, w.Code, w.Body.String())
		}

		if got := w.Header().Get("Content-Type"); got != want {
			t.Errorf("upstream %q: Content-Type = %q, want %q", in, got, want)
		}
	}
}

// digestTTLFixture caches a manifest and a layer by digest through a proxy
// whose digest TTL is ttl and returns their cache keys.
func digestTTLFixture(t *testing.T, ttl time.Duration) (*ociFixture, []string) {
	t.Helper()

	f := newOCIFixture(t)
	f.svc.WithDockerDigestTTL(ttl)

	dg := f.reg.addManifest("library/alpine", "3.20", alpineManifest)
	layer := f.reg.addBlob("library/alpine", "layer-bytes")

	for _, p := range []string{"manifests/3.20", "manifests/" + dg, "blobs/" + layer} {
		if w := f.do(http.MethodGet, "hub/library/alpine/"+p); w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", p, w.Code)
		}
	}

	return f, []string{"library/alpine/manifests/" + dg, "library/alpine/blobs/" + layer}
}

func TestOCI_DigestEntriesExpireAfterTheDigestTTL(t *testing.T) {
	const ttl = 48 * time.Hour

	f, digestKeys := digestTTLFixture(t, ttl)

	for _, key := range digestKeys {
		e := f.db.entry("docker-1", key)
		if e == nil {
			t.Fatalf("%s not cached", key)
		}

		if left := time.Until(e.ExpiresAt); left > ttl || left < ttl-time.Minute {
			t.Errorf("%s expires in %v, want about %v so the cache worker can evict it", key, left, ttl)
		}
	}

	// A tag keeps the repository's cache TTL: only digests are capped here.
	if tag := f.db.entry("docker-1", "library/alpine/manifests/3.20"); time.Until(tag.ExpiresAt) > time.Hour {
		t.Errorf("tag expires in %v, want the repository TTL (10m)", time.Until(tag.ExpiresAt))
	}
}

func TestOCI_PullRenewsDigestEntriesPastHalfTheirLife(t *testing.T) {
	const ttl = 48 * time.Hour

	tests := []struct {
		name      string
		remaining time.Duration
		renewed   bool
	}{
		{"fresh entry is left alone", ttl - time.Hour, false},
		{"just under half the life renews", ttl/2 - time.Minute, true},
		{"nearly expired renews", time.Minute, true},
		{"entry from before digest eviction renews", 100 * 365 * 24 * time.Hour, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, keys := digestTTLFixture(t, ttl)
			key := keys[1] // the layer

			e := f.db.entry("docker-1", key)
			set := time.Now().Add(tc.remaining)
			e.ExpiresAt = set

			if err := f.db.UpsertProxyCacheEntry(context.Background(), e); err != nil {
				t.Fatal(err)
			}

			layer := strings.TrimPrefix(key, "library/alpine/blobs/")
			if w := f.do(http.MethodGet, "hub/library/alpine/blobs/"+layer); w.Code != http.StatusOK || w.Body.String() != "layer-bytes" {
				t.Fatalf("pull = %d %q", w.Code, w.Body.String())
			}

			got := f.db.entry("docker-1", key).ExpiresAt

			if tc.renewed {
				if left := time.Until(got); left > ttl || left < ttl-time.Minute {
					t.Fatalf("expires in %v after the pull, want renewed to about %v", left, ttl)
				}

				return
			}

			if !got.Equal(set) {
				t.Fatalf("expiry moved from %v to %v, want it left alone", set, got)
			}
		})
	}
}

func TestOCI_PullLeavesOpenEndedEntriesAloneWithoutDigestTTL(t *testing.T) {
	f, keys := digestTTLFixture(t, 0)
	key := keys[1]

	before := f.db.entry("docker-1", key).ExpiresAt

	layer := strings.TrimPrefix(key, "library/alpine/blobs/")
	if w := f.do(http.MethodGet, "hub/library/alpine/blobs/"+layer); w.Code != http.StatusOK {
		t.Fatalf("pull = %d", w.Code)
	}

	if after := f.db.entry("docker-1", key).ExpiresAt; !after.Equal(before) {
		t.Fatalf("expiry changed from %v to %v with the digest TTL disabled", before, after)
	}
}
