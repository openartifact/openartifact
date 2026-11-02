// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
)

const (
	hfOld   = "1111111111111111111111111111111111111111"
	hfNew   = "2222222222222222222222222222222222222222"
	hfCfg   = `{"model_type":"bert"}`
	hfLarge = "large-weights-bytes"
)

func gitBlobID(body string) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\x00%s", len(body), body)

	return hex.EncodeToString(h.Sum(nil))
}

// hfFake is a Hub with the model acme/m: two commits (hfOld four days old, hfNew
// an hour old), a small file with a git blob etag and an LFS file with a sha256
// etag, announced the way the Hub does (a redirect carrying the headers).
type hfFake struct {
	*fixture

	mu   sync.Mutex
	hits map[string]int
	auth map[string]string
}

func (f *hfFake) hit(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.hits[path]
}

func newHFFake(t *testing.T) *hfFake {
	t.Helper()

	f := &hfFake{hits: map[string]int{}, auth: map[string]string{}}
	now := time.Now().UTC()
	oldDate := now.Add(-96 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	newDate := now.Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	sum := sha256.Sum256([]byte(hfLarge))
	files := map[string]struct{ etag, body string }{
		"config.json":  {gitBlobID(hfCfg), hfCfg},
		"weights.bin":  {hex.EncodeToString(sum[:]), hfLarge},
		"bad.json":     {gitBlobID("expected"), "different"},
		"nested/a.txt": {gitBlobID("a"), "a"},
	}

	f.fixture = newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.Method+" "+r.URL.Path]++
		f.auth[r.URL.Path] = r.Header.Get("Authorization")
		f.mu.Unlock()

		p := r.URL.Path

		switch {
		case p == "/api/models/acme/m/revision/main" || p == "/api/models/acme/m" || p == "/api/models/acme/m/revision/"+hfNew:
			_, _ = fmt.Fprintf(w, `{"id":"acme/m","sha":%q,"lastModified":%q}`, hfNew, newDate)
		case p == "/api/models/acme/m/revision/old" || p == "/api/models/acme/m/revision/"+hfOld:
			_, _ = fmt.Fprintf(w, `{"id":"acme/m","sha":%q,"lastModified":%q}`, hfOld, oldDate)
		case p == "/api/models/acme/m/commits/"+hfNew:
			_, _ = fmt.Fprintf(w, `[{"id":%q,"date":%q},{"id":%q,"date":%q}]`, hfNew, newDate, hfOld, oldDate)
		case strings.HasPrefix(p, "/api/models/acme/m/tree/"):
			w.Header().Set("Link", `<https://hub.example/api/models/acme/m/tree/x?cursor=NEXT123>; rel="next"`)
			_, _ = fmt.Fprint(w, `[{"type":"file","path":"config.json","size":21,"xetHash":"abc"},`+
				`{"type":"file","path":"weights.bin","xetHash":"def"}]`)
		case strings.HasPrefix(p, "/api/models/acme/gated") || p == "/gated/resolve/main/x":
			w.Header().Set("X-Error-Code", "GatedRepo")
			w.Header().Set("X-Error-Message", "restricted")
			w.WriteHeader(http.StatusUnauthorized)
		case strings.HasPrefix(p, "/api/"):
			w.Header().Set("X-Error-Code", "RevisionNotFound")
			http.NotFound(w, r)
		case strings.HasPrefix(p, "/acme/m/resolve/"):
			rest := strings.TrimPrefix(p, "/acme/m/resolve/")
			rev, name, _ := strings.Cut(rest, "/")

			file, ok := files[name]
			if !ok || (rev != hfNew && rev != hfOld) {
				w.Header().Set("X-Error-Code", "EntryNotFound")
				http.NotFound(w, r)

				return
			}

			w.Header().Set("X-Repo-Commit", rev)
			w.Header().Set("X-Linked-Etag", `"`+file.etag+`"`)
			w.Header().Set("X-Linked-Size", strconv.Itoa(len(file.body)))
			w.Header().Set("X-Xet-Hash", "abc")

			if r.Method == http.MethodHead {
				w.Header().Set("Location", "/cdn/"+rest)
				w.WriteHeader(http.StatusTemporaryRedirect)

				return
			}

			http.Redirect(w, r, "/cdn/"+rest, http.StatusTemporaryRedirect)
		case strings.HasPrefix(p, "/cdn/"):
			_, name, _ := strings.Cut(strings.TrimPrefix(p, "/cdn/"), "/")

			f.mu.Lock()
			body := files[name].body
			f.mu.Unlock()

			_, _ = fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	})
	f.repo.Format = "huggingface"
	f.repo.UpstreamURL = f.upstream.URL

	return f
}

func (f *hfFake) get(method, path string) *httptest.ResponseRecorder {
	f.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, "/repos/hf/"+path, nil)
	w := httptest.NewRecorder()
	f.svc.Serve(w, req, f.repo, path)

	return w
}

func TestHuggingFace_ResolvePinsTheRefAndCachesTheFilePerCommit(t *testing.T) {
	f := newHFFake(t)

	for i := range 2 {
		w := f.get(http.MethodGet, "acme/m/resolve/main/config.json")
		if w.Code != http.StatusOK || w.Body.String() != hfCfg {
			t.Fatalf("get %d: %d %q", i, w.Code, w.Body.String())
		}

		if got := w.Header().Get("X-Repo-Commit"); got != hfNew {
			t.Errorf("X-Repo-Commit = %q, want %q", got, hfNew)
		}
	}

	if n := f.hit("GET /cdn/" + hfNew + "/config.json"); n != 1 {
		t.Errorf("file fetched %d times, want once", n)
	}

	// The commit is its own revision: no ref lookup is needed for it, and the
	// file is not fetched again.
	if w := f.get(http.MethodGet, "acme/m/resolve/"+hfNew+"/config.json"); w.Code != http.StatusOK {
		t.Fatalf("by commit: %d", w.Code)
	}

	if n := f.hit("GET /cdn/" + hfNew + "/config.json"); n != 1 {
		t.Errorf("file fetched %d times after the commit request, want once", n)
	}
}

func TestHuggingFace_HeadAnswersTheClientsMetadataWithoutXet(t *testing.T) {
	f := newHFFake(t)

	w := f.get(http.MethodHead, "acme/m/resolve/main/weights.bin")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	sum := sha256.Sum256([]byte(hfLarge))
	h := w.Header()

	for name, want := range map[string]string{
		"X-Repo-Commit":  hfNew,
		"X-Linked-Etag":  `"` + hex.EncodeToString(sum[:]) + `"`,
		"X-Linked-Size":  strconv.Itoa(len(hfLarge)),
		"Content-Length": strconv.Itoa(len(hfLarge)),
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	for name := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-xet") || name == "Link" || name == "Location" {
			t.Errorf("header %s must not be passed on", name)
		}
	}
}

func TestHuggingFace_BodyThatDoesNotMatchItsEtagIsRefusedAndNotCached(t *testing.T) {
	f := newHFFake(t)

	for range 2 {
		if w := f.get(http.MethodGet, "acme/m/resolve/main/bad.json"); w.Code != http.StatusBadGateway {
			t.Fatalf("status %d, want 502", w.Code)
		}
	}

	if n := f.hit("GET /cdn/" + hfNew + "/bad.json"); n != 2 {
		t.Errorf("refused file fetched %d times, want 2 (never cached)", n)
	}
}

func TestHuggingFace_MinAgeMovesARefBackToTheNewestOldEnoughCommit(t *testing.T) {
	f := newHFFake(t)
	f.repo.MinAgeHours = 24

	w := f.get(http.MethodGet, "acme/m/resolve/main/config.json")
	if w.Code != http.StatusOK || w.Header().Get("X-Repo-Commit") != hfOld {
		t.Fatalf("main: %d commit %q, want %s", w.Code, w.Header().Get("X-Repo-Commit"), hfOld)
	}

	info := f.get(http.MethodGet, "api/models/acme/m")
	if !strings.Contains(info.Body.String(), hfOld) || strings.Contains(info.Body.String(), hfNew) {
		t.Errorf("info shows %s", info.Body.String())
	}

	if w := f.get(http.MethodGet, "acme/m/resolve/"+hfNew+"/config.json"); w.Code != http.StatusForbidden {
		t.Errorf("explicit young commit: %d, want 403", w.Code)
	}

	f.repo.MinAgeHours = 1000

	if w := f.get(http.MethodGet, "acme/m/resolve/main/config.json"); w.Code != http.StatusForbidden {
		t.Errorf("nothing old enough: %d, want 403", w.Code)
	}
}

func TestHuggingFace_CurationBlocksACommitByModelAndVersion(t *testing.T) {
	f := newHFFake(t)
	f.useCuration("denylist", blockRule("acme/m", hfNew))

	if w := f.get(http.MethodGet, "acme/m/resolve/"+hfNew+"/config.json"); w.Code != http.StatusForbidden {
		t.Errorf("blocked commit: %d, want 403", w.Code)
	}

	// The package name is case-insensitive, like the Hub's.
	if w := f.get(http.MethodGet, "ACME/m/resolve/"+hfNew+"/config.json"); w.Code != http.StatusForbidden {
		t.Errorf("blocked commit under another spelling: %d, want 403", w.Code)
	}

	if w := f.get(http.MethodGet, "acme/m/resolve/old/config.json"); w.Code != http.StatusOK {
		t.Errorf("other commit: %d, want 200", w.Code)
	}
}

func TestHuggingFace_ErrorsCarryTheCodesTheClientMaps(t *testing.T) {
	f := newHFFake(t)

	for _, tc := range []struct {
		path   string
		status int
		code   string
	}{
		{"acme/m/resolve/nope/config.json", http.StatusNotFound, "RevisionNotFound"},
		{"acme/m/resolve/main/missing.txt", http.StatusNotFound, "EntryNotFound"},
		{"api/models/acme/gated", http.StatusUnauthorized, "GatedRepo"},
		{"api/models/acme/m/tree/nope", http.StatusNotFound, "RevisionNotFound"},
	} {
		w := f.get(http.MethodGet, tc.path)
		if w.Code != tc.status || w.Header().Get("X-Error-Code") != tc.code {
			t.Errorf("%s: %d %q, want %d %q", tc.path, w.Code, w.Header().Get("X-Error-Code"), tc.status, tc.code)
		}
	}
}

func TestHuggingFace_TreeDropsXetHashesAndPagesThroughTheProxy(t *testing.T) {
	f := newHFFake(t)

	w := f.get(http.MethodGet, "api/models/acme/m/tree/main")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	if strings.Contains(w.Body.String(), "xetHash") {
		t.Errorf("xetHash passed on: %s", w.Body.String())
	}

	var entries []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil || len(entries) != 2 {
		t.Fatalf("body %s: %v", w.Body.String(), err)
	}

	link := w.Header().Get("Link")
	if !strings.Contains(link, "cursor=NEXT123") || !strings.HasSuffix(link, `rel="next"`) || strings.Contains(link, "hub.example") {
		t.Errorf("Link = %q", link)
	}
}

func TestHuggingFace_TokenGoesToTheUpstreamAsBearer(t *testing.T) {
	t.Setenv(crypto.EnvKey, "hf-test-key-0123456789abcdef0123456789")
	crypto.InitFromEnv()

	f := newHFFake(t)

	enc, err := crypto.Encrypt("hf_secret")
	if err != nil {
		t.Fatal(err)
	}

	f.repo.UpstreamUsername = "x"
	f.repo.UpstreamPasswordEnc = enc

	if w := f.get(http.MethodGet, "acme/m/resolve/main/config.json"); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if got := f.auth["/api/models/acme/m/revision/main"]; got != "Bearer hf_secret" {
		t.Errorf("info request Authorization = %q", got)
	}

	if got := f.auth["/acme/m/resolve/"+hfNew+"/config.json"]; got != "Bearer hf_secret" {
		t.Errorf("file request Authorization = %q", got)
	}
}

func TestParseHF(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want hfRequest
		ok   bool
	}{
		{"gpt2/resolve/main/config.json", hfRequest{hfFile, "models", "gpt2", "main", "config.json"}, true},
		{"a/b/resolve/refs%2Fpr%2F1/onnx/m.onnx", hfRequest{hfFile, "models", "a/b", "refs/pr/1", "onnx/m.onnx"}, true},
		{"datasets/a/b/resolve/main/x.parquet", hfRequest{hfFile, "datasets", "a/b", "main", "x.parquet"}, true},
		{"api/spaces/a/b", hfRequest{hfInfo, "spaces", "a/b", "", ""}, true},
		{"api/models/a/b/revision/main", hfRequest{hfInfo, "models", "a/b", "main", ""}, true},
		{"api/models/a/b/tree/main/sub/dir", hfRequest{hfTree, "models", "a/b", "main", "sub/dir"}, true},
		{"api/models/a/b/tree/main", hfRequest{hfTree, "models", "a/b", "main", ""}, true},
		{"a/b/resolve/main/../x", hfRequest{}, false},
		{"a/b/resolve/main/", hfRequest{}, false},
		{"a/b/c/resolve/main/x", hfRequest{}, false},
		{"api/models/a/b/c/d", hfRequest{}, false},
		{"api/whoami-v2", hfRequest{}, false},
		{"api/models/a/b/revision/..%2Fx", hfRequest{}, false},
		{"a/b/raw/main/x", hfRequest{}, false},
	} {
		got, ok := parseHF(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseHF(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
