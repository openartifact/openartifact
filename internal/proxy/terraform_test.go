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
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/format/terraform"
)

const (
	tfTestBase   = "https://oa.test"
	tfTestSecret = "0123456789abcdef0123456789abcdef"

	tfZipFile  = "terraform-provider-random_3.5.1_linux_amd64.zip"
	tfSumsFile = "terraform-provider-random_3.5.1_SHA256SUMS"
	tfSigFile  = "terraform-provider-random_3.5.1_SHA256SUMS.72D7468F.sig"
)

// tfCfg is what the fake registry and the fake release host answer; tests
// change it between requests.
type tfCfg struct {
	moduleGet  string // X-Terraform-Get; "" = the module archive on the release host
	opentofu   bool   // answer module downloads as 200 {"location": ...}
	moduleBody string
	zipBody    string
	shasum     string // package document shasum; "" = sha256 of zipBody
	sumsLine   string // SHA256SUMS content; "" = the right line
	zipPath    string // download_url path on the release host
	registry   int    // status of every registry request when not 0
	keys       string
}

type tfFake struct {
	t       *testing.T
	fixture *fixture
	release *httptest.Server

	mu       sync.Mutex
	cfg      tfCfg
	registry map[string]int // path -> hits at the registry
	hosted   map[string]int // path -> hits at the release host
	auth     map[string]string
}

func (f *tfFake) set(fn func(*tfCfg)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn(&f.cfg)
}

func (f *tfFake) config() tfCfg {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.cfg
}

func (f *tfFake) registryHits(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.registry[path]
}

func (f *tfFake) releaseHits(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.hosted[path]
}

func hexOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

const tfKeys = `{"gpg_public_keys":[{"key_id":"72D7468F","ascii_armor":"-----BEGIN PGP PUBLIC KEY BLOCK-----\nx\n-----END PGP PUBLIC KEY BLOCK-----"}]}`

// newTFFake serves a registry for the namespace "ns" (module ns/demo/aws,
// provider random) and, on another host, the archives it points at.
func newTFFake(t *testing.T) *tfFake {
	t.Helper()

	f := &tfFake{t: t, registry: map[string]int{}, hosted: map[string]int{}, auth: map[string]string{}}
	f.cfg = tfCfg{
		moduleBody: "PK\x03\x04 module archive",
		zipBody:    "PK\x03\x04 provider archive",
		zipPath:    "/releases/" + tfZipFile,
		keys:       tfKeys,
	}

	f.release = httptest.NewServer(http.HandlerFunc(f.serveReleases))
	t.Cleanup(f.release.Close)

	f.fixture = newFixture(t, f.serveRegistry)
	f.fixture.repo.Format = "terraform"
	f.fixture.repo.Name = "ns"
	f.fixture.repo.UpstreamURL = f.fixture.upstream.URL
	f.fixture.svc.WithTerraformTicketKey([]byte(tfTestSecret))
	f.fixture.svc.SetBaseURL(tfTestBase)

	return f
}

func (f *tfFake) serveReleases(w http.ResponseWriter, r *http.Request) {
	cfg := f.config()

	f.mu.Lock()
	f.hosted[r.URL.Path]++
	f.auth[r.URL.Path] = r.Header.Get("Authorization")
	f.mu.Unlock()

	switch r.URL.Path {
	case "/modules/demo.zip", "/modules/demo":
		_, _ = fmt.Fprint(w, cfg.moduleBody)
	case "/redirect/zip":
		http.Redirect(w, r, "/releases/"+tfZipFile, http.StatusFound)
	case "/releases/" + tfZipFile, "/alt/" + tfZipFile:
		_, _ = fmt.Fprint(w, cfg.zipBody)
	case "/releases/" + tfSumsFile:
		line := cfg.sumsLine
		if line == "" {
			line = hexOf(
				cfg.zipBody,
			) + "  " + tfZipFile + "\n" + strings.Repeat(
				"a",
				64,
			) + "  terraform-provider-random_3.5.1_darwin_arm64.zip\n"
		}

		_, _ = fmt.Fprint(w, line)
	case "/releases/" + tfSigFile:
		_, _ = w.Write([]byte{0x89, 0x01, 0x02, 0x03})
	default:
		http.NotFound(w, r)
	}
}

func (f *tfFake) serveRegistry(w http.ResponseWriter, r *http.Request) {
	cfg := f.config()

	f.mu.Lock()
	f.registry[r.URL.Path]++
	f.auth["registry"+r.URL.Path] = r.Header.Get("Authorization")
	f.mu.Unlock()

	if cfg.registry != 0 {
		w.WriteHeader(cfg.registry)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/.well-known/terraform.json":
		_, _ = fmt.Fprint(w, `{"modules.v1":"/v1/modules/","providers.v1":"/v1/providers"}`)
	case "/v1/modules/ns/demo/aws/versions":
		_, _ = fmt.Fprint(
			w,
			`{"modules":[{"source":"ns/demo/aws","versions":[{"version":"1.1.0"},{"version":"1.0.0"},{"version":"1.0.0-rc.1"},{"version":"../evil"},{"version":"1.0.0"}]}]}`,
		)
	case "/v1/modules/ns/demo/aws/1.0.0/download":
		get := cfg.moduleGet
		if get == "" {
			get = f.release.URL + "/modules/demo.zip"
		}

		if cfg.opentofu {
			_, _ = fmt.Fprintf(w, `{"location":%q}`, get)
			return
		}

		w.Header().Set("X-Terraform-Get", get)
		w.WriteHeader(http.StatusNoContent)
	case "/v1/providers/ns/random/versions":
		_, _ = fmt.Fprint(
			w,
			`{"versions":[{"version":"3.5.1","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"},{"os":"darwin","arch":"arm64"}]},{"version":"3.4.0","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"}]},{"version":"bad version","platforms":[{"os":"linux","arch":"amd64"}]}]}`,
		)
	case "/v1/providers/ns/random/3.5.1/download/linux/amd64":
		shasum := cfg.shasum
		if shasum == "" {
			shasum = hexOf(cfg.zipBody)
		}

		_, _ = fmt.Fprintf(
			w,
			`{"protocols":["5.0"],"os":"linux","arch":"amd64","filename":%q,"download_url":%q,"shasums_url":%q,"shasums_signature_url":%q,"shasum":%q,"signing_keys":%s}`,
			tfZipFile,
			f.release.URL+cfg.zipPath,
			f.release.URL+"/releases/"+tfSumsFile,
			f.release.URL+"/releases/"+tfSigFile,
			shasum,
			cfg.keys,
		)
	default:
		http.NotFound(w, r)
	}
}

func (f *tfFake) get(path string) *httptest.ResponseRecorder {
	f.t.Helper()

	return f.fixture.do(http.MethodGet, path)
}

// redeem plays the download of a ticket the proxy minted.
func (f *tfFake) redeem(d terraform.Download) *httptest.ResponseRecorder {
	f.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/terraform/dl/ticket/"+d.File, nil)
	w := httptest.NewRecorder()
	f.fixture.svc.ServeTerraformDownload(w, req, f.fixture.repo, d)

	return w
}

func tfProviderDownload(kind terraform.DownloadKind, file string) terraform.Download {
	return terraform.Download{Kind: kind, A: "random", B: "3.5.1", C: "linux_amd64", File: file}
}

func tfModuleRedeem(file string) terraform.Download {
	return terraform.Download{Kind: terraform.DownloadModule, A: "demo", B: "aws", C: "1.0.0", File: file}
}

// ticketFile returns the file name of a minted ticket URL on the server base.
func ticketFile(t *testing.T, link string) string {
	t.Helper()

	rest, ok := strings.CutPrefix(link, "/terraform/dl/")
	if !ok {
		t.Fatalf("%q is not a path-absolute ticket URL", link)
	}

	_, file, ok := strings.Cut(rest, "/")
	if !ok || file == "" {
		t.Fatalf("%q has no file name", link)
	}

	return file
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

func decodeJSON[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("not JSON (%v): %s", err, w.Body)
	}

	return v
}

// ---- versions ----

func TestTerraformProviderVersionsAreCachedAndCleaned(t *testing.T) {
	f := newTFFake(t)

	w := f.get("random/versions")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	doc := decodeJSON[tfProviderVersionList](t, w)
	if len(doc.Versions) != 2 || doc.Versions[0].Version != "3.4.0" || doc.Versions[1].Version != "3.5.1" ||
		len(doc.Versions[1].Platforms) != 2 {
		t.Fatalf("versions %+v", doc.Versions)
	}

	// The relative providers.v1 of the discovery document was resolved.
	if f.registryHits("/v1/providers/ns/random/versions") != 1 || f.registryHits("/.well-known/terraform.json") != 1 {
		t.Fatalf("registry hits: %v", f.registry)
	}

	if w := f.get("random/versions"); w.Code != http.StatusOK {
		t.Fatalf("second status %d", w.Code)
	}

	if f.registryHits("/v1/providers/ns/random/versions") != 1 || f.registryHits("/.well-known/terraform.json") != 1 {
		t.Fatalf("a cached list was fetched again: %v", f.registry)
	}
}

func TestTerraformModuleVersionsDetailsAndLatest(t *testing.T) {
	f := newTFFake(t)

	w := f.get("demo/aws/versions")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	out := decodeJSON[tfModuleVersionsOut](t, w)
	if len(out.Modules) != 1 || out.Modules[0].Source != "ns/demo/aws" {
		t.Fatalf("modules %+v", out.Modules)
	}

	var got []string
	for _, v := range out.Modules[0].Versions {
		got = append(got, v.Version)
	}

	if strings.Join(got, ",") != "1.0.0-rc.1,1.0.0,1.1.0" {
		t.Fatalf("versions %v: invalid ones must be dropped, the rest sorted", got)
	}

	if w := f.get("demo/aws/1.0.0"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"ns/demo/aws/1.0.0"`) {
		t.Fatalf("details %d: %s", w.Code, w.Body)
	}

	if w := f.get("demo/aws"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"version":"1.1.0"`) {
		t.Fatalf("latest %d: %s", w.Code, w.Body)
	}

	if w := f.get(
		"demo/aws/download",
	); w.Code != http.StatusFound ||
		!strings.HasSuffix(w.Header().Get("Location"), "/demo/aws/1.1.0/download") {
		t.Fatalf("latest download %d, Location %q", w.Code, w.Header().Get("Location"))
	}

	if w := f.get("demo/aws/9.9.9"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown version: %d", w.Code)
	}

	if w := f.get("nope/aws/versions"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"errors"`) {
		t.Fatalf("unknown module: %d %s", w.Code, w.Body)
	}

	if f.registryHits("/v1/modules/ns/demo/aws/versions") != 1 {
		t.Fatalf("version list fetched %d times", f.registryHits("/v1/modules/ns/demo/aws/versions"))
	}
}

func TestTerraformUnroutablePathsAre404(t *testing.T) {
	f := newTFFake(t)

	for _, p := range []string{"", "a", "a/b/c/d/e/f", "../x/versions", "demo/versions/versions", "x/1.0.0/download/linux", "demo/aws/1.0.0/other"} {
		if w := f.get(p); w.Code != http.StatusNotFound {
			t.Errorf("%q: status %d, want 404", p, w.Code)
		}
	}

	if f.fixture.hits.Load() != 0 {
		t.Errorf("the upstream was contacted %d times for unroutable paths", f.fixture.hits.Load())
	}
}

func TestTerraformInvalidNamespaceIsRefused(t *testing.T) {
	f := newTFFake(t)
	f.fixture.repo.Name = "Not A Namespace!"

	if w := f.get("random/versions"); w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}

	if f.fixture.hits.Load() != 0 {
		t.Fatal("the upstream was contacted")
	}
}

func TestTerraformStaleOnUpstreamFailure(t *testing.T) {
	f := newTFFake(t)

	if w := f.get("random/versions"); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	f.set(func(c *tfCfg) { c.registry = http.StatusInternalServerError })
	f.fixture.db.expire(f.fixture.repo.ID, tfProviderVersionsKey("random"))

	w := f.get("random/versions")
	if w.Code != http.StatusOK || w.Header().Get("Warning") == "" {
		t.Fatalf("stale answer: status %d, Warning %q", w.Code, w.Header().Get("Warning"))
	}

	f.fixture.db.expireAgo(f.fixture.repo.ID, tfProviderVersionsKey("random"), 2*3600*1e9)
	f.fixture.repo.MaxStaleSeconds = 60

	if w := f.get("random/versions"); w.Code != http.StatusBadGateway {
		t.Fatalf("past max-stale: status %d, want 502", w.Code)
	}
}

func TestTerraformRegistryOutageWithoutCacheIs502(t *testing.T) {
	f := newTFFake(t)
	f.set(func(c *tfCfg) { c.registry = http.StatusInternalServerError })

	if w := f.get("random/versions"); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"errors"`) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestTerraformPrivateUpstreamBlockedByDefault(t *testing.T) {
	f := newTFFake(t)
	t.Setenv(allowPrivateEnv, "")

	if w := f.get("random/versions"); w.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", w.Code)
	}

	if f.fixture.hits.Load() != 0 {
		t.Fatalf("loopback registry contacted %d times", f.fixture.hits.Load())
	}
}

func TestTerraformUpstreamCredentialsStayOnTheRegistry(t *testing.T) {
	f := newTFFake(t)
	f.fixture.repo.UpstreamUsername = "ci"
	f.fixture.repo.UpstreamPasswordEnc = "s3cret"

	if w := f.get("demo/aws/1.0.0/download"); w.Code != http.StatusNoContent {
		t.Fatalf("download: %d %s", w.Code, w.Body)
	}

	if f.redeem(tfModuleRedeem("demo-aws-1.0.0.zip")).Code != http.StatusOK {
		t.Fatal("redeem failed")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.auth["registry/v1/modules/ns/demo/aws/1.0.0/download"] == "" {
		t.Error("the registry got no credentials")
	}

	if got := f.auth["/modules/demo.zip"]; got != "" {
		t.Errorf("the archive host (another origin) got credentials: %q", got)
	}
}

// ---- modules ----

func TestTerraformModuleDownloadThroughTicket(t *testing.T) {
	f := newTFFake(t)

	w := f.get("demo/aws/1.0.0/download")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	file := ticketFile(t, w.Header().Get("X-Terraform-Get"))
	if file != "demo-aws-1.0.0.zip" {
		t.Fatalf("ticket file %q", file)
	}

	for i := range 2 {
		got := f.redeem(tfModuleRedeem(file))
		if got.Code != http.StatusOK || got.Body.String() != f.config().moduleBody ||
			got.Header().Get("Content-Type") != "application/zip" {
			t.Fatalf("redeem %d: status %d type %q body %q", i, got.Code, got.Header().Get("Content-Type"), got.Body)
		}
	}

	if f.releaseHits("/modules/demo.zip") != 1 {
		t.Fatalf("archive fetched %d times, want once and then cached", f.releaseHits("/modules/demo.zip"))
	}

	if wrong := f.redeem(tfModuleRedeem("other.zip")); wrong.Code != http.StatusNotFound {
		t.Fatalf("another file name: %d", wrong.Code)
	}
}

func TestTerraformModuleArchiveHintAndOpenTofuForm(t *testing.T) {
	f := newTFFake(t)
	f.set(func(c *tfCfg) {
		c.opentofu = true
		c.moduleGet = f.release.URL + "/modules/demo?archive=zip"
	})

	w := f.get("demo/aws/1.0.0/download")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	if got := f.redeem(tfModuleRedeem(ticketFile(t, w.Header().Get("X-Terraform-Get")))); got.Code != http.StatusOK {
		t.Fatalf("redeem %d: %s", got.Code, got.Body)
	}

	if f.releaseHits("/modules/demo") != 1 {
		t.Fatalf("hint URL fetched %d times", f.releaseHits("/modules/demo"))
	}
}

func TestTerraformModuleGetterSourcesAreRefused(t *testing.T) {
	for _, get := range []string{
		"git::https://gitlab.com/org/repo?ref=v1.0.0",
		"s3::https://s3.amazonaws.com/bucket/demo.zip",
		"hg::https://example.org/demo",
		"gitlab.com/org/repo",
		"git@github.com:org/repo.git",
		"git::ssh://git@github.com/org/repo.git",
		"git::https://github.com/org/repo?sshkey=abc",
		"https://example.org/demo.tar.bz2//subdir",
		"https://example.org/demo.zip?checksum=abc",
		"https://example.org/blob",
		"ftp://example.org/demo.zip",
	} {
		f := newTFFake(t)
		f.set(func(c *tfCfg) { c.moduleGet = get })

		w := f.get("demo/aws/1.0.0/download")
		if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "plain archive URLs") ||
			!strings.Contains(w.Body.String(), "documentation") {
			t.Errorf("%q: status %d, body %s; want 501 with a docs pointer", get, w.Code, w.Body)
		}

		if w.Header().Get("X-Terraform-Get") != "" {
			t.Errorf("%q: a refused source minted a ticket", get)
		}
	}
}

func TestTerraformModuleArchiveThatIsNotAnArchiveIsNotStored(t *testing.T) {
	f := newTFFake(t)
	f.set(func(c *tfCfg) { c.moduleBody = "<html>login</html>" })

	w := f.get("demo/aws/1.0.0/download")
	file := ticketFile(t, w.Header().Get("X-Terraform-Get"))

	if got := f.redeem(tfModuleRedeem(file)); got.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", got.Code)
	}

	if f.fixture.db.entry(f.fixture.repo.ID, tfModuleArchiveKey("demo", "aws", "1.0.0", file)) != nil {
		t.Fatal("a non-archive was cached")
	}
}

func TestTerraformModuleArchiveIsImmutableOnceCached(t *testing.T) {
	f := newTFFake(t)
	repoID := f.fixture.repo.ID

	w := f.get("demo/aws/1.0.0/download")
	file := ticketFile(t, w.Header().Get("X-Terraform-Get"))
	key := tfModuleArchiveKey("demo", "aws", "1.0.0", file)

	if got := f.redeem(tfModuleRedeem(file)); got.Code != http.StatusOK {
		t.Fatalf("first fetch %d", got.Code)
	}

	first := f.fixture.db.entry(repoID, key).BlobKey

	f.set(func(c *tfCfg) { c.moduleBody = "PK\x03\x04 tampered" })
	f.fixture.db.expire(repoID, key)
	// The stale copy may be served on failure, but never replaced.
	got := f.redeem(tfModuleRedeem(file))

	if cur := f.fixture.db.entry(repoID, key); cur == nil || cur.BlobKey != first {
		t.Fatalf("the cached archive changed: %+v", cur)
	}

	if got.Code == http.StatusOK && got.Body.String() != "PK\x03\x04 module archive" {
		t.Fatalf("served the changed archive: %q", got.Body)
	}
}

func TestTerraformDownloadsNeedATicketKey(t *testing.T) {
	f := newTFFake(t)
	f.fixture.svc.WithTerraformTicketKey(nil)

	if w := f.get("demo/aws/1.0.0/download"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("module download status %d, want 503", w.Code)
	}

	if w := f.get("random/3.5.1/download/linux/amd64"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("package status %d, want 503", w.Code)
	}
}

// ---- providers ----

// The package document's URLs are path-absolute references: never an origin,
// the path prefix of an explicit base URL only.
func TestTerraformProviderPackageURLsCarryNoOrigin(t *testing.T) {
	for _, tc := range []struct{ baseURL, prefix string }{
		{"", ""},
		{tfTestBase, ""},
		{tfTestBase + "/pre/", "/pre"},
	} {
		t.Run(tc.baseURL, func(t *testing.T) {
			f := newTFFake(t)
			f.fixture.svc.SetBaseURL(tc.baseURL)

			w := f.get("random/3.5.1/download/linux/amd64")
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}

			if w.Header().Get("ETag") != "" {
				t.Errorf("a ticket document must not carry a validator, got %q", w.Header().Get("ETag"))
			}

			doc := decodeJSON[tfPackageDoc](t, w)
			for name, link := range map[string]string{"download_url": doc.DownloadURL, "shasums_url": doc.ShasumsURL, "sig": doc.ShasumsSignatureURL} {
				if !strings.HasPrefix(link, tc.prefix+"/terraform/dl/") || strings.Contains(link, "://") {
					t.Errorf("%s = %q, want a path-absolute reference under %q", name, link, tc.prefix)
				}
			}
		})
	}
}

func TestTerraformProviderPackageRewritesURLsAndKeepsKeys(t *testing.T) {
	f := newTFFake(t)

	w := f.get("random/3.5.1/download/linux/amd64")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("Cache-Control %q", w.Header().Get("Cache-Control"))
	}

	doc := decodeJSON[tfPackageDoc](t, w)
	if doc.Filename != tfZipFile || doc.Shasum != hexOf(f.config().zipBody) || doc.OS != "linux" || doc.Arch != "amd64" {
		t.Fatalf("document %+v", doc)
	}

	for name, link := range map[string]string{"download_url": doc.DownloadURL, "shasums_url": doc.ShasumsURL, "shasums_signature_url": doc.ShasumsSignatureURL} {
		if !strings.HasPrefix(link, "/terraform/dl/") {
			t.Errorf("%s = %q is not a ticket URL of this server", name, link)
		}
	}

	if ticketFile(t, doc.DownloadURL) != tfZipFile || ticketFile(t, doc.ShasumsURL) != tfSumsFile ||
		ticketFile(t, doc.ShasumsSignatureURL) != tfSigFile {
		t.Errorf("ticket files: %q %q %q", doc.DownloadURL, doc.ShasumsURL, doc.ShasumsSignatureURL)
	}

	// The upstream's own signing keys, unchanged.
	var want, got any

	_ = json.Unmarshal([]byte(tfKeys), &want)
	_ = json.Unmarshal(doc.SigningKeys, &got)

	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Fatalf("signing_keys %s changed", doc.SigningKeys)
	}
}

func TestTerraformProviderFilesThroughTickets(t *testing.T) {
	f := newTFFake(t)
	zip := f.config().zipBody

	if f.get("random/3.5.1/download/linux/amd64").Code != http.StatusOK {
		t.Fatal("package document failed")
	}

	got := f.redeem(tfProviderDownload(terraform.DownloadProviderZip, tfZipFile))
	if got.Code != http.StatusOK || got.Body.String() != zip {
		t.Fatalf("zip: status %d body %q", got.Code, got.Body)
	}

	sums := f.redeem(tfProviderDownload(terraform.DownloadProviderSums, tfSumsFile))
	if sums.Code != http.StatusOK || !strings.Contains(sums.Body.String(), hexOf(zip)+"  "+tfZipFile) {
		t.Fatalf("sums: status %d body %q", sums.Code, sums.Body)
	}

	sig := f.redeem(tfProviderDownload(terraform.DownloadProviderSumsSig, tfSigFile))
	if sig.Code != http.StatusOK || sig.Body.String() != "\x89\x01\x02\x03" {
		t.Fatalf("signature: status %d body %q", sig.Code, sig.Body)
	}

	// A second round is served from the cache.
	f.redeem(tfProviderDownload(terraform.DownloadProviderZip, tfZipFile))
	f.redeem(tfProviderDownload(terraform.DownloadProviderSums, tfSumsFile))

	if f.releaseHits(f.config().zipPath) != 1 || f.releaseHits("/releases/"+tfSumsFile) < 1 {
		t.Fatalf("release host hits: %v", f.hosted)
	}

	if f.releaseHits(f.config().zipPath) != 1 {
		t.Fatalf("zip fetched %d times", f.releaseHits(f.config().zipPath))
	}

	if wrong := f.redeem(tfProviderDownload(terraform.DownloadProviderZip, "evil.zip")); wrong.Code != http.StatusNotFound {
		t.Fatalf("another file name: %d", wrong.Code)
	}
}

func TestTerraformProviderDownloadFollowsRedirects(t *testing.T) {
	f := newTFFake(t)
	f.set(func(c *tfCfg) { c.zipPath = "/redirect/zip" })

	if f.get("random/3.5.1/download/linux/amd64").Code != http.StatusOK {
		t.Fatal("package document failed")
	}

	if got := f.redeem(
		tfProviderDownload(terraform.DownloadProviderZip, tfZipFile),
	); got.Code != http.StatusOK ||
		got.Body.String() != f.config().zipBody {
		t.Fatalf("status %d body %q", got.Code, got.Body)
	}
}

func TestTerraformProviderZipThatMismatchesTheShasumIsNotStored(t *testing.T) {
	f := newTFFake(t)
	f.set(func(c *tfCfg) { c.shasum = strings.Repeat("0", 64) })

	if f.get("random/3.5.1/download/linux/amd64").Code != http.StatusOK {
		t.Fatal("package document failed")
	}

	if got := f.redeem(tfProviderDownload(terraform.DownloadProviderZip, tfZipFile)); got.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", got.Code)
	}

	if e := f.fixture.db.entry(f.fixture.repo.ID, tfZipKey("random", "3.5.1", "linux_amd64", tfZipFile)); e != nil {
		t.Fatalf("a zip failing its shasum was cached: %+v", e)
	}
}

func TestTerraformProviderSumsFileMustAgreeWithTheDocument(t *testing.T) {
	for name, sums := range map[string]string{
		"another digest":  strings.Repeat("b", 64) + "  " + tfZipFile + "\n",
		"file not listed": strings.Repeat("a", 64) + "  other.zip\n",
		"malformed":       "not a checksum file\n",
	} {
		f := newTFFake(t)
		f.set(func(c *tfCfg) { c.sumsLine = sums })

		if f.get("random/3.5.1/download/linux/amd64").Code != http.StatusOK {
			t.Fatalf("%s: package document failed", name)
		}

		if got := f.redeem(tfProviderDownload(terraform.DownloadProviderZip, tfZipFile)); got.Code != http.StatusBadGateway {
			t.Errorf("%s: zip status %d, want 502", name, got.Code)
		}

		if e := f.fixture.db.entry(f.fixture.repo.ID, tfZipKey("random", "3.5.1", "linux_amd64", tfZipFile)); e != nil {
			t.Errorf("%s: the zip was cached", name)
		}
	}
}

func TestTerraformProviderNotAZipIsNotStored(t *testing.T) {
	f := newTFFake(t)
	f.set(func(c *tfCfg) { c.zipBody = "<html>nope</html>" })

	if f.get("random/3.5.1/download/linux/amd64").Code != http.StatusOK {
		t.Fatal("package document failed")
	}

	if got := f.redeem(tfProviderDownload(terraform.DownloadProviderZip, tfZipFile)); got.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", got.Code)
	}
}

func TestTerraformRepublishedProviderDropsTheCachedZip(t *testing.T) {
	f := newTFFake(t)
	repoID := f.fixture.repo.ID

	f.get("random/3.5.1/download/linux/amd64")

	if f.redeem(tfProviderDownload(terraform.DownloadProviderZip, tfZipFile)).Code != http.StatusOK {
		t.Fatal("first download failed")
	}

	zipKey := tfZipKey("random", "3.5.1", "linux_amd64", tfZipFile)
	if f.fixture.db.entry(repoID, zipKey) == nil {
		t.Fatal("zip not cached")
	}

	// The upstream republishes the platform: new bytes, new shasum.
	f.set(func(c *tfCfg) { c.zipBody = "PK\x03\x04 republished" })
	f.fixture.db.expire(repoID, tfPackageKey("random", "3.5.1", "linux", "amd64"))

	w := f.get("random/3.5.1/download/linux/amd64")
	if w.Code != http.StatusOK {
		t.Fatalf("package status %d: %s", w.Code, w.Body)
	}

	if f.fixture.db.entry(repoID, zipKey) != nil {
		t.Fatal("the zip of the old shasum is still cached")
	}

	if got := f.redeem(
		tfProviderDownload(terraform.DownloadProviderZip, tfZipFile),
	); got.Code != http.StatusOK ||
		got.Body.String() != "PK\x03\x04 republished" {
		t.Fatalf("status %d body %q", got.Code, got.Body)
	}
}

func TestTerraformBlockedAndWrites(t *testing.T) {
	f := newTFFake(t)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/", nil)
	w := httptest.NewRecorder()
	f.fixture.svc.Serve(w, req, f.fixture.repo, "random/versions")

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: %d", w.Code)
	}

	f.fixture.repo.Blocked = true

	if got := f.redeem(tfModuleRedeem("demo-aws-1.0.0.zip")); got.Code != http.StatusForbidden {
		t.Fatalf("blocked redeem: %d", got.Code)
	}

	f.fixture.repo.Blocked = false
	f.fixture.repo.Format = "maven"

	if got := f.redeem(tfModuleRedeem("demo-aws-1.0.0.zip")); got.Code != http.StatusNotFound {
		t.Fatalf("redeem against another format: %d", got.Code)
	}
}

// ---- pure parsers ----

func TestParseTerraformPath(t *testing.T) {
	for in, want := range map[string]tfPath{
		"random/versions":                       {kind: tfProviderVersions, name: "random"},
		"/random/versions":                      {kind: tfProviderVersions, name: "random"},
		"demo/aws":                              {kind: tfModuleLatest, name: "demo", system: "aws"},
		"demo/aws/versions":                     {kind: tfModuleVersions, name: "demo", system: "aws"},
		"demo/aws/download":                     {kind: tfModuleLatestDownload, name: "demo", system: "aws"},
		"demo/aws/1.2.3":                        {kind: tfModuleVersion, name: "demo", system: "aws", version: "1.2.3"},
		"demo/aws/1.2.3/download":               {kind: tfModuleDownload, name: "demo", system: "aws", version: "1.2.3"},
		"random/3.5.1/download/linux/amd64":     {kind: tfProviderPackage, name: "random", version: "3.5.1", os: "linux", arch: "amd64"},
		"random/3.5.1-rc.1/download/darwin/arm": {kind: tfProviderPackage, name: "random", version: "3.5.1-rc.1", os: "darwin", arch: "arm"},
	} {
		got, ok := parseTerraformPath(in)
		if !ok || got != want {
			t.Errorf("%q = %+v, %v; want %+v", in, got, ok, want)
		}
	}

	for _, in := range []string{
		"", "/", "a/b/c/d/e/f", "../versions", "a/../versions", "demo/versions/versions", "demo/aws/latest", "demo/aws/1.2.3/downloads",
		"random/x/download/linux/amd64", "random/1.0.0/download/Linux/amd64", "random/1.0.0/download/linux/amd64/", "demo//aws", "demo/aws/1.2.3/%2e%2e",
	} {
		if got, ok := parseTerraformPath(in); ok {
			t.Errorf("%q accepted as %+v", in, got)
		}
	}
}

func TestParseTerraformDiscovery(t *testing.T) {
	doc := mustURL(t, "https://registry.example/.well-known/terraform.json")

	ep, err := parseTerraformDiscovery([]byte(`{"modules.v1":"/v1/modules/","providers.v1":"https://providers.example/p"}`), doc)
	if err != nil {
		t.Fatal(err)
	}

	if ep.modules.String() != "https://registry.example/v1/modules/" || ep.providers.String() != "https://providers.example/p/" {
		t.Fatalf("endpoints %v %v", ep.modules, ep.providers)
	}

	if ep, err := parseTerraformDiscovery(
		[]byte(`{"providers.v1":"v1/providers/"}`),
		doc,
	); err != nil || ep.modules != nil ||
		ep.providers.String() != "https://registry.example/.well-known/v1/providers/" {
		t.Fatalf("providers only: %+v %v", ep, err)
	}

	for _, bad := range []string{
		``, `[]`, `{}`, `{"login.v1":{}}`, `{"modules.v1":5}`, `{"modules.v1":"ftp://x/"}`,
		`{"modules.v1":"https://u:p@x/"}`, `{"modules.v1":"https://x/?a=b"}`, `{"modules.v1":"http://%zz"}`,
	} {
		if _, err := parseTerraformDiscovery([]byte(bad), doc); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseModuleGet(t *testing.T) {
	base := mustURL(t, "https://registry.example/v1/modules/ns/demo/aws/1.0.0/download")

	for _, tc := range []struct {
		in, url, ext string
	}{
		{"https://h.example/demo.zip", "https://h.example/demo.zip", "zip"},
		{"https://h.example/demo.tar.gz", "https://h.example/demo.tar.gz", "tar.gz"},
		{"https://h.example/demo.TGZ", "https://h.example/demo.TGZ", "tgz"},
		{"https://h.example/demo?archive=zip", "https://h.example/demo", "zip"},
		{"https://h.example/dl?sig=a%2Fb&archive=tar.gz&x=1", "https://h.example/dl?sig=a%2Fb&x=1", "tar.gz"},
		{"/modules/demo.zip", "https://registry.example/modules/demo.zip", "zip"},
		{"  https://h.example/demo.zip\n", "https://h.example/demo.zip", "zip"},
	} {
		got, err := parseModuleGet(tc.in, base, tfCodeloadBase)
		if err != nil || got.url != tc.url || got.ext != tc.ext {
			t.Errorf("%q = %+v, %v; want %s (%s)", tc.in, got, err, tc.url, tc.ext)
		}
	}

	for _, in := range []string{
		"git::https://gitlab.com/org/repo", "s3::https://x/y.zip", "gcs::https://x/y.zip", "hg::https://x/y", "gitlab.com/org/repo",
		"git@github.com:org/repo.git", "file:///etc/passwd", "https://h/demo.zip?checksum=md5:abc",
		"https://h/demo?ref=main", "https://h/demo", "https://h/demo.exe", "https://u:p@h/demo.zip", "https://h/demo.zip#x", "http://",
	} {
		if _, err := parseModuleGet(in, base, tfCodeloadBase); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("%q: error %v, want an unsupported-source error", in, err)
		}
	}

	for _, in := range []string{"", "   ", strings.Repeat("a", tfMaxGet+1)} {
		if _, err := parseModuleGet(in, base, tfCodeloadBase); err == nil || strings.Contains(err.Error(), "unsupported") {
			t.Errorf("%q: error %v, want a plain error", truncate(in, 20), err)
		}
	}
}

func TestParseSHA256SUMS(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("B", 64)

	got, err := parseSHA256SUMS([]byte(a + "  x.zip\r\n\n" + b + " *y.zip\n"))
	if err != nil || got["x.zip"] != a || got["y.zip"] != strings.ToLower(b) || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}

	for _, bad := range []string{"", "\n\n", "x\n", a + "\n", a + "  \n", a + "  dir/x.zip\n", strings.Repeat("z", 64) + "  x.zip\n", a + "  x.zip\n" + b + "  x.zip\n", strings.Repeat(a+"  x\n", tfMaxSums)} {
		if _, err := parseSHA256SUMS([]byte(bad)); err == nil {
			t.Errorf("%q accepted", truncate(bad, 30))
		}
	}

	// The same name twice with the same digest vouches for nothing new but is consistent.
	if _, err := parseSHA256SUMS([]byte(a + "  x.zip\n" + a + "  x.zip\n")); err != nil {
		t.Errorf("a repeated consistent line was refused: %v", err)
	}
}

func TestCanonVersions(t *testing.T) {
	out, err := canonModuleVersions(
		strings.NewReader(`{"modules":[{"versions":[{"version":"2.0.0"},{"version":"1.0.0"},{"version":"1.0.0"},{"version":"x y"}]}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	versions, err := parseCanonModuleVersions(out)
	if err != nil || strings.Join(versions, ",") != "1.0.0,2.0.0" {
		t.Fatalf("%v, %v", versions, err)
	}

	for _, bad := range []string{`{}`, `{"modules":[]}`, `{"modules":[{"versions":[{"version":"bad version"}]}]}`, `nope`} {
		if _, err := canonModuleVersions(strings.NewReader(bad)); err == nil {
			t.Errorf("module list %q accepted", bad)
		}
	}

	if _, err := canonProviderVersions(
		strings.NewReader(`{"versions":[{"version":"1.0.0","platforms":[{"os":"Linux","arch":"amd64"}]}]}`),
	); err == nil {
		t.Error("a provider version without a valid platform was kept")
	}

	if _, err := canonProviderVersions(strings.NewReader(`[`)); err == nil {
		t.Error("broken provider list accepted")
	}
}

func TestParseProviderPackageRefusals(t *testing.T) {
	docURL := mustURL(t, "https://registry.example/v1/providers/ns/random/1.0.0/download/linux/amd64")
	good := map[string]any{
		"os":                    "linux",
		"arch":                  "amd64",
		"filename":              "p_1.0.0_linux_amd64.zip",
		"shasum":                strings.Repeat("a", 64),
		"download_url":          "https://rel.example/p_1.0.0_linux_amd64.zip",
		"shasums_url":           "/sums/p_1.0.0_SHA256SUMS",
		"shasums_signature_url": "https://rel.example/p_1.0.0_SHA256SUMS.sig",
	}

	encode := func(mod func(map[string]any)) []byte {
		m := maps.Clone(good)

		mod(m)

		b, _ := json.Marshal(m)

		return b
	}

	doc, err := parseProviderPackage(encode(func(map[string]any) {}), docURL, "linux", "amd64")
	if err != nil || doc.ShasumsURL != "https://registry.example/sums/p_1.0.0_SHA256SUMS" {
		t.Fatalf("good document: %+v, %v", doc, err)
	}

	for name, mod := range map[string]func(map[string]any){
		"wrong platform":  func(m map[string]any) { m["os"] = "darwin" },
		"path in name":    func(m map[string]any) { m["filename"] = "../x.zip" },
		"not a zip":       func(m map[string]any) { m["filename"] = "x.tar" },
		"short shasum":    func(m map[string]any) { m["shasum"] = "abc" },
		"non hex shasum":  func(m map[string]any) { m["shasum"] = strings.Repeat("z", 64) },
		"file URL":        func(m map[string]any) { m["download_url"] = "file:///etc/passwd" },
		"missing sums":    func(m map[string]any) { delete(m, "shasums_url") },
		"same sums + sig": func(m map[string]any) { m["shasums_signature_url"] = m["shasums_url"] },
		"sig is the zip":  func(m map[string]any) { m["shasums_signature_url"] = m["download_url"] },
		"keys not object": func(m map[string]any) { m["signing_keys"] = "x" },
		"userinfo":        func(m map[string]any) { m["download_url"] = "https://u:p@rel.example/x.zip" },
	} {
		if _, err := parseProviderPackage(encode(mod), docURL, "linux", "amd64"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}

	if _, err := parseProviderPackage([]byte(`{`), docURL, "linux", "amd64"); err == nil {
		t.Error("broken JSON accepted")
	}
}

func TestVerifyTFArchive(t *testing.T) {
	for ext, body := range map[string]string{
		"zip": "PK\x03\x04x", "tar.gz": "\x1f\x8b\x08", "tgz": "\x1f\x8b\x08", "tar.bz2": "BZh9", "tar.xz": "\xfd7zXZ\x00", "tar": "data",
	} {
		if err := verifyTFArchive(ext, strings.NewReader(body)); err != nil {
			t.Errorf("%s: %v", ext, err)
		}
	}

	for ext, body := range map[string]string{
		"zip": "<html>", "tar.gz": "PK", "tar.bz2": "x", "tar.xz": "x", "tar": "<html>", "rar": "Rar!",
	} {
		if err := verifyTFArchive(ext, strings.NewReader(body)); err == nil {
			t.Errorf("%s accepted %q", ext, body)
		}
	}

	if err := verifyTFArchive("zip", strings.NewReader("")); err == nil {
		t.Error("an empty body is not an archive")
	}
}

func TestTerraformCacheKeyClassification(t *testing.T) {
	zipKey := tfZipKey("random", "3.5.1", "linux_amd64", tfZipFile)
	modKey := tfModuleArchiveKey("demo", "aws", "1.0.0", "demo-aws-1.0.0.zip")

	if !tfProviderZipKey(zipKey) || tfProviderZipKey(modKey) || tfProviderZipKey(tfProviderVersionsKey("random")) {
		t.Error("provider zip classification")
	}

	if !tfArchiveKey(zipKey) || !tfArchiveKey(modKey) || tfArchiveKey(tfPackageKey("random", "3.5.1", "linux", "amd64")) {
		t.Error("archive classification")
	}

	if !Immutable("terraform", zipKey) || Immutable("terraform", modKey) {
		t.Error("only provider zips are pinned immutable")
	}

	if isMetadataKey("terraform", zipKey) || isMetadataKey("terraform", modKey) ||
		!isMetadataKey("terraform", tfModuleVersionsKey("demo", "aws")) {
		t.Error("metadata classification")
	}
}
