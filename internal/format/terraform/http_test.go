// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/gpg"
)

const registryHost = "http://registry.test"

func jsonOf[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, w.Body.String())
	}

	return v
}

func hexSHA(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

// ticketPathOf turns an absolute ticket URL into the path a client requests.
func ticketPathOf(t *testing.T, link string) string {
	t.Helper()

	path, ok := strings.CutPrefix(link, registryHost)
	if !ok || !strings.HasPrefix(path, ticketPath) {
		t.Fatalf("%q is not a ticket URL on %s", link, registryHost)
	}

	return path
}

func TestDiscovery(t *testing.T) {
	for _, tc := range []struct{ baseURL, want string }{
		{"http://registry.test", "/repos/"},
		{"http://registry.test/", "/repos/"},
		{"https://example.com/artifacts", "/artifacts/repos/"},
		{"", "/repos/"},
	} {
		t.Run(tc.baseURL, func(t *testing.T) {
			e := newEnv(t)
			e.h.BaseURL = tc.baseURL

			w := e.do(http.MethodGet, "/.well-known/terraform.json", nil)
			if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("status %d, content type %q", w.Code, w.Header().Get("Content-Type"))
			}

			doc := jsonOf[map[string]string](t, w)
			if doc["modules.v1"] != tc.want || doc["providers.v1"] != tc.want || len(doc) != 2 {
				t.Fatalf("discovery = %v, want both services at %q", doc, tc.want)
			}
		})
	}
}

// moduleFixture is a repository holding vpc/aws 1.0.0 (a zip) and 1.10.0,
// 1.2.0 and 2.0.0-rc.1 (tar.gz archives).
type moduleFixture struct {
	e       *env
	archive []byte
}

func newModuleFixture(t *testing.T) *moduleFixture {
	t.Helper()

	e := newEnv(t)
	archive := moduleZip(t)

	w := e.putModule("vpc", "aws", "1.0.0", archive)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}

	created := jsonOf[map[string]any](t, w)
	if created["sha256"] != hexSHA(archive) || created["version"] != "1.0.0" || created["name"] != "vpc" || created["system"] != "aws" {
		t.Fatalf("upload response = %v", created)
	}

	for _, v := range []string{"1.10.0", "1.2.0", "2.0.0-rc.1"} {
		if w := e.putModule("vpc", "aws", v, tarGzOf(t, tarEntry{name: "main.tf", body: v})); w.Code != http.StatusCreated {
			t.Fatalf("upload %s: %d %s", v, w.Code, w.Body)
		}
	}

	return &moduleFixture{e: e, archive: archive}
}

func TestModuleUploadAnnouncesItself(t *testing.T) {
	f := newModuleFixture(t)

	events := f.e.pub.Events()
	if len(events) != 4 || events[0].Type != webhook.EventArtifactUploaded {
		t.Fatalf("events = %+v", events)
	}

	if a := events[0].Artifact(); a["name"] != "modules/vpc/aws" || a["version"] != "1.0.0" || a["path"] != "modules/vpc/aws/1.0.0" {
		t.Fatalf("webhook artifact = %v", a)
	}
}

func TestModuleVersionsInPrecedenceOrder(t *testing.T) {
	f := newModuleFixture(t)
	e := f.e

	w := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/versions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	got := jsonOf[struct {
		Modules []struct {
			Source   string `json:"source"`
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"modules"`
	}](t, w)

	if len(got.Modules) != 1 || got.Modules[0].Source != "myrepo/vpc/aws" {
		t.Fatalf("modules = %+v", got.Modules)
	}

	var versions []string
	for _, v := range got.Modules[0].Versions {
		versions = append(versions, v.Version)
	}

	if strings.Join(versions, " ") != "1.0.0 1.2.0 1.10.0 2.0.0-rc.1" {
		t.Fatalf("versions = %v", versions)
	}
}

func TestModuleLatestPrefersNewestStable(t *testing.T) {
	f := newModuleFixture(t)
	e := f.e

	w := e.do(http.MethodGet, "/repos/myrepo/vpc/aws", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	got := jsonOf[map[string]any](t, w)
	if got["version"] != "1.10.0" || got["id"] != "myrepo/vpc/aws/1.10.0" || got["namespace"] != "myrepo" || got["provider"] != "aws" {
		t.Fatalf("latest = %v", got)
	}

	w = e.do(http.MethodGet, "/repos/myrepo/vpc/aws/download", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/repos/myrepo/vpc/aws/1.10.0/download" {
		t.Fatalf("download redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestModuleVersionDetails(t *testing.T) {
	f := newModuleFixture(t)
	e := f.e

	w := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/1.2.0", nil)
	if w.Code != http.StatusOK || jsonOf[map[string]any](t, w)["version"] != "1.2.0" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestModuleDownloadMintsTicketForArchive(t *testing.T) {
	f := newModuleFixture(t)
	e := f.e
	archive := f.archive

	w := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/1.0.0/download", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}

	link := w.Header().Get("X-Terraform-Get")
	if !strings.HasPrefix(link, registryHost+ticketPath) || !strings.HasSuffix(link, "/vpc-aws-1.0.0.zip") {
		t.Fatalf("X-Terraform-Get = %q", link)
	}

	// The client sends no credentials to this URL: the bare router must serve it.
	anon := httptest.NewRecorder()
	req := formattest.Request(http.MethodGet, ticketPathOf(t, link), nil)
	e.router().ServeHTTP(anon, req)

	if anon.Code != http.StatusOK || !bytes.Equal(anon.Body.Bytes(), archive) {
		t.Fatalf("ticket download: %d, %d bytes (want %d)", anon.Code, anon.Body.Len(), len(archive))
	}

	if ct := anon.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type = %q", ct)
	}

	head := httptest.NewRecorder()
	e.router().ServeHTTP(head, formattest.Request(http.MethodHead, ticketPathOf(t, link), nil))

	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != anon.Header().Get("Content-Length") {
		t.Fatalf("HEAD: %d, body %d, length %q", head.Code, head.Body.Len(), head.Header().Get("Content-Length"))
	}

	if got := e.db.mods[0].DownloadCount; got != 1 {
		t.Fatalf("download count = %d, want 1 (counted once, at the authenticated call)", got)
	}
}

func TestModuleTarGzDownload(t *testing.T) {
	f := newModuleFixture(t)
	e := f.e

	w := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/1.2.0/download", nil)
	link := w.Header().Get("X-Terraform-Get")

	if w.Code != http.StatusNoContent || !strings.HasSuffix(link, "/vpc-aws-1.2.0.tar.gz") {
		t.Fatalf("status %d, link %q", w.Code, link)
	}

	got := e.do(http.MethodGet, ticketPathOf(t, link), nil)
	if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("status %d, type %q", got.Code, got.Header().Get("Content-Type"))
	}
}

func TestModuleUnknownCoordinates(t *testing.T) {
	f := newModuleFixture(t)
	e := f.e

	for _, path := range []string{
		"/repos/myrepo/nope/aws/versions",
		"/repos/myrepo/vpc/aws/9.9.9",
		"/repos/myrepo/vpc/aws/9.9.9/download",
		"/repos/myrepo/vpc/azure",
		"/repos/myrepo/vpc/aws/not-a-version",
		"/repos/myrepo/vpc/aws/1.0.0+build/download",
	} {
		if w := e.do(http.MethodGet, path, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}
}

func TestTicketRoutes(t *testing.T) {
	e := newEnv(t)
	archive := moduleZip(t)

	if w := e.putModule("vpc", "aws", "1.0.0", archive); w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}

	mint := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/1.0.0/download", nil)
	path := ticketPathOf(t, mint.Header().Get("X-Terraform-Get"))
	parts := strings.Split(path, "/")
	ticketID, filename := parts[3], parts[4]

	for _, tc := range []struct {
		name    string
		path    string
		method  string
		advance time.Duration
		want    int
	}{
		{"valid", path, http.MethodGet, 0, http.StatusOK},
		{"valid just before expiry", path, http.MethodGet, ticketTTL - time.Second, http.StatusOK},
		{"expired", path, http.MethodGet, ticketTTL, http.StatusForbidden},
		{"tampered ticket", ticketPath + ticketID[:len(ticketID)-2] + "xx/" + filename, http.MethodGet, 0, http.StatusForbidden},
		{"garbage ticket", ticketPath + "garbage/" + filename, http.MethodGet, 0, http.StatusForbidden},
		{"another file name", ticketPath + ticketID + "/other.zip", http.MethodGet, 0, http.StatusNotFound},
		{"HEAD of an expired ticket", path, http.MethodHead, ticketTTL, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.now = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC).Add(tc.advance)

			w := e.do(tc.method, tc.path, nil)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}

			if tc.want == http.StatusForbidden && strings.Contains(strings.ToLower(w.Body.String()), "expire") {
				t.Fatalf("a refused ticket must not say why: %q", w.Body)
			}
		})
	}

	t.Run("a deleted version is gone", func(t *testing.T) {
		e.now = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

		if w := e.do(http.MethodDelete, "/repos/myrepo/_api/modules/vpc/aws/1.0.0", nil); w.Code != http.StatusOK {
			t.Fatalf("delete: %d %s", w.Code, w.Body)
		}

		if w := e.do(http.MethodGet, path, nil); w.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404", w.Code)
		}
	})

	t.Run("no ticket key means no downloads", func(t *testing.T) {
		e2 := newEnv(t)
		e2.h.WithTicketKey(nil)

		if w := e2.putModule("vpc", "aws", "1.0.0", archive); w.Code != http.StatusCreated {
			t.Fatalf("upload: %d", w.Code)
		}

		if w := e2.do(http.MethodGet, "/repos/myrepo/vpc/aws/1.0.0/download", nil); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("mint status %d, want 503", w.Code)
		}

		if w := e2.do(http.MethodGet, path, nil); w.Code != http.StatusForbidden {
			t.Fatalf("ticket status %d, want 403", w.Code)
		}
	})
}

func TestVersionsCaching(t *testing.T) {
	e := newEnv(t)
	e.db.Repo.Public = true

	if w := e.putModule("vpc", "aws", "1.0.0", moduleZip(t)); w.Code != http.StatusCreated {
		t.Fatalf("upload: %d", w.Code)
	}

	if w := e.putProvider("aws", "1.0.0", "linux", "amd64", providerZip(t, "aws", "1.0.0"), ""); w.Code != http.StatusCreated {
		t.Fatalf("provider upload: %d %s", w.Code, w.Body)
	}

	anon := &auth.Principal{IsAnonymous: true, Credential: auth.CredentialNone}
	user := &auth.Principal{Username: "alice", Credential: auth.CredentialNone}

	for _, url := range []string{"/repos/myrepo/vpc/aws/versions", "/repos/myrepo/aws/versions"} {
		t.Run(url, func(t *testing.T) {
			first := e.doAs(anon, http.MethodGet, url, nil)
			if first.Code != http.StatusOK {
				t.Fatalf("status %d", first.Code)
			}

			etag := first.Header().Get("ETag")
			if etag == "" || !strings.Contains(first.Header().Get("Cache-Control"), "public") || first.Header().Get("Vary") == "" {
				t.Fatalf("headers = %v", first.Header())
			}

			if w := e.doAs(user, http.MethodGet, url, nil); strings.Contains(w.Header().Get("Cache-Control"), "public") {
				t.Fatalf("an authenticated response is cacheable by shared caches: %q", w.Header().Get("Cache-Control"))
			}

			if w := e.doAs(anon, http.MethodGet, url, nil, "If-None-Match", etag); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
				t.Fatalf("revalidation = %d with %d bytes", w.Code, w.Body.Len())
			}
		})
	}

	t.Run("the validator moves with the version set", func(t *testing.T) {
		before := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/versions", nil).Header().Get("ETag")

		if w := e.putModule("vpc", "aws", "1.1.0", moduleZip(t)); w.Code != http.StatusCreated {
			t.Fatalf("upload: %d", w.Code)
		}

		w := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/versions", nil, "If-None-Match", before)
		if w.Code != http.StatusOK || w.Header().Get("ETag") == before {
			t.Fatalf("a stale validator was honoured: %d", w.Code)
		}
	})

	t.Run("private repository is never public", func(t *testing.T) {
		e.db.Repo.Public = false

		if w := e.doAs(
			anon,
			http.MethodGet,
			"/repos/myrepo/vpc/aws/versions",
			nil,
		); strings.Contains(
			w.Header().Get("Cache-Control"),
			"public",
		) {
			t.Fatalf("Cache-Control = %q", w.Header().Get("Cache-Control"))
		}
	})
}

func TestModuleUploadErrors(t *testing.T) {
	e := newEnv(t)

	if w := e.putModule("vpc", "aws", "1.0.0", moduleZip(t)); w.Code != http.StatusCreated {
		t.Fatalf("first upload: %d", w.Code)
	}

	for _, tc := range []struct {
		name    string
		path    string
		body    []byte
		want    int
		wantRow int
	}{
		{"same version again", "vpc/aws/1.0.0", moduleZip(t), http.StatusConflict, 1},
		{"v prefix", "vpc/aws/v1.0.0", moduleZip(t), http.StatusBadRequest, 1},
		{"short version", "vpc/aws/1.0", moduleZip(t), http.StatusBadRequest, 1},
		{"build metadata", "vpc/aws/1.0.1+b", moduleZip(t), http.StatusBadRequest, 1},
		{"reserved name", "packages/aws/1.0.0", moduleZip(t), http.StatusBadRequest, 1},
		{"reserved system", "vpc/versions/1.0.0", moduleZip(t), http.StatusBadRequest, 1},
		{"dotted name", "v.pc/aws/1.0.0", moduleZip(t), http.StatusBadRequest, 1},
		{"empty body", "vpc/aws/2.0.0", nil, http.StatusBadRequest, 1},
		{"not an archive", "vpc/aws/2.0.0", []byte("hello"), http.StatusBadRequest, 1},
		{"traversal", "vpc/aws/2.0.0", zipOf(t, [2]string{"../x.tf", "x"}), http.StatusBadRequest, 1},
		{"no tf files", "vpc/aws/2.0.0", zipOf(t, [2]string{"README", "x"}), http.StatusUnprocessableEntity, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blobs := len(e.bs.Blobs)
			used := e.db.used

			w := e.do(http.MethodPut, "/repos/myrepo/_api/modules/"+tc.path, bytes.NewReader(tc.body))
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}

			if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") ||
				len(jsonOf[map[string][]string](t, w)["errors"]) == 0 {
				t.Fatalf("error body is not the registry error shape: %s", w.Body)
			}

			if len(e.db.mods) != tc.wantRow || len(e.bs.Blobs) != blobs || e.db.used != used {
				t.Fatalf("rejected upload changed state: rows=%d blobs=%d used=%d", len(e.db.mods), len(e.bs.Blobs), e.db.used)
			}
		})
	}

	t.Run("declared size over the limit", func(t *testing.T) {
		req := formattest.Request(http.MethodPut, "/repos/myrepo/_api/modules/vpc/aws/3.0.0", bytes.NewReader(moduleZip(t)))
		req.ContentLength = maxModuleBytes + 1

		w := httptest.NewRecorder()
		e.router().ServeHTTP(w, req)

		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status %d, want 413", w.Code)
		}
	})

	t.Run("quota", func(t *testing.T) {
		e.db.limit = e.db.used

		w := e.putModule("vpc", "aws", "4.0.0", moduleZip(t))
		if w.Code != http.StatusInsufficientStorage {
			t.Fatalf("status %d, want 507: %s", w.Code, w.Body)
		}

		if len(e.db.mods) != 1 {
			t.Fatalf("rows = %d after a refused upload", len(e.db.mods))
		}

		e.db.limit = 0
	})
}

func TestUploadToAnotherFormatIsRefused(t *testing.T) {
	e := newEnv(t)
	e.db.Repo.Format = "helm"

	if w := e.putModule("vpc", "aws", "1.0.0", moduleZip(t)); w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}

	if len(e.db.mods) != 0 || len(e.bs.Blobs) != 0 {
		t.Fatal("terraform rows were created in a helm repository")
	}

	for _, path := range []string{"/repos/myrepo/vpc/aws", "/repos/myrepo/vpc/aws/versions", "/repos/myrepo/aws/versions"} {
		if w := e.do(http.MethodGet, path, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}

	if w := e.do(http.MethodGet, "/repos/nope/vpc/aws/versions", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown repository = %d", w.Code)
	}
}

func TestLatestGuardPassesOtherRoutesThrough(t *testing.T) {
	e := newEnv(t)
	passed := false

	next := e.h.LatestGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true }))

	for _, tc := range []struct {
		name, method, path string
		wantPass           bool
	}{
		{"conda repodata", http.MethodGet, "/repos/myrepo/linux-64/repodata.json", true},
		{"three segments", http.MethodGet, "/repos/myrepo/a/b/c", true},
		{"one segment", http.MethodGet, "/repos/myrepo/vpc", true},
		{"a write", http.MethodPut, "/repos/myrepo/vpc/aws", true},
		{"dotted file", http.MethodGet, "/repos/myrepo/linux-64/pkg-1.0.tar.bz2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passed = false

			rr := httptest.NewRecorder()
			next.ServeHTTP(rr, formattest.Request(tc.method, tc.path, nil))

			if passed != tc.wantPass {
				t.Fatalf("passed = %v, want %v", passed, tc.wantPass)
			}
		})
	}
}

func TestDeleteModule(t *testing.T) {
	e := newEnv(t)

	for _, v := range []string{"1.0.0", "1.1.0-rc.1"} {
		if w := e.putModule("vpc", "aws", v, moduleZip(t)); w.Code != http.StatusCreated {
			t.Fatalf("upload: %d", w.Code)
		}
	}

	if e.db.artifacts != 2 {
		t.Fatalf("artifacts = %d, want 2", e.db.artifacts)
	}

	t.Run("immutable releases", func(t *testing.T) {
		e.db.Repo.ImmutableReleases = true
		defer func() { e.db.Repo.ImmutableReleases = false }()

		if w := e.do(http.MethodDelete, "/repos/myrepo/_api/modules/vpc/aws/1.0.0", nil); w.Code != http.StatusConflict {
			t.Fatalf("release delete = %d, want 409", w.Code)
		}

		if len(e.db.mods) != 2 {
			t.Fatal("an immutable release was deleted")
		}
	})

	t.Run("delete", func(t *testing.T) {
		w := e.do(http.MethodDelete, "/repos/myrepo/_api/modules/vpc/aws/1.0.0", nil)
		if w.Code != http.StatusOK || jsonOf[map[string]int](t, w)["deleted"] != 1 {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}

		if len(e.db.mods) != 1 || e.db.artifacts != 1 {
			t.Fatalf("rows=%d artifacts=%d after a delete", len(e.db.mods), e.db.artifacts)
		}

		if w := e.do(http.MethodDelete, "/repos/myrepo/_api/modules/vpc/aws/1.0.0", nil); w.Code != http.StatusNotFound {
			t.Fatalf("second delete = %d, want 404", w.Code)
		}

		events := e.pub.Events()
		if last := events[len(events)-1]; last.Type != webhook.EventArtifactDeleted {
			t.Fatalf("last event = %s", last.Type)
		}
	})

	t.Run("a prerelease is deletable while releases are immutable", func(t *testing.T) {
		e.db.Repo.ImmutableReleases = true
		defer func() { e.db.Repo.ImmutableReleases = false }()

		if w := e.do(http.MethodDelete, "/repos/myrepo/_api/modules/vpc/aws/1.1.0-rc.1", nil); w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
	})
}

// providerPackage is the package description a client reads before it
// downloads a provider.
type providerPackage struct {
	Protocols   []string `json:"protocols"`
	OS          string   `json:"os"`
	Arch        string   `json:"arch"`
	Filename    string   `json:"filename"`
	DownloadURL string   `json:"download_url"`
	SumsURL     string   `json:"shasums_url"`
	SigURL      string   `json:"shasums_signature_url"`
	Shasum      string   `json:"shasum"`
	SigningKeys struct {
		Keys []struct {
			KeyID string `json:"key_id"`
			Armor string `json:"ascii_armor"`
		} `json:"gpg_public_keys"`
	} `json:"signing_keys"`
}

// providerFixture is a signed repository holding aws 1.2.3 for linux/amd64 and
// darwin/arm64 and a 1.3.0-beta.1, with the description of the linux package
// already fetched.
type providerFixture struct {
	e             *env
	signer        *gpg.LocalSigner
	linux, darwin []byte
	pkg           providerPackage
}

func newProviderFixture(t *testing.T) *providerFixture {
	t.Helper()

	f := &providerFixture{e: newEnv(t), signer: newSigner(t)}
	f.e.h.WithSigner(signerResolver{f.signer})
	f.linux, f.darwin = providerZip(t, "aws", "1.2.3"), providerZip(t, "aws", "1.2.3", [2]string{"LICENSE2", "x"})

	if w := f.e.putProvider(
		"aws",
		"1.2.3",
		"linux",
		"amd64",
		f.linux,
		"?protocols=5.0,6.0",
		checksumHeader,
		hexSHA(f.linux),
	); w.Code != http.StatusCreated {
		t.Fatalf("linux upload: %d %s", w.Code, w.Body)
	}

	w := f.e.putProvider("aws", "1.2.3", "darwin", "arm64", f.darwin, "?protocols=6.0,5.0")
	if w.Code != http.StatusCreated {
		t.Fatalf("darwin upload: %d %s", w.Code, w.Body)
	}

	if got := jsonOf[map[string]any](t, w); got["sha256"] != hexSHA(f.darwin) || got["os"] != "darwin" {
		t.Fatalf("upload response = %v", got)
	}

	if w := f.e.putProvider(
		"aws",
		"1.3.0-beta.1",
		"linux",
		"amd64",
		providerZip(t, "aws", "1.3.0-beta.1"),
		"",
	); w.Code != http.StatusCreated {
		t.Fatalf("beta upload: %d %s", w.Code, w.Body)
	}

	desc := f.e.do(http.MethodGet, "/repos/myrepo/aws/1.2.3/download/linux/amd64", nil)
	if desc.Code != http.StatusOK {
		t.Fatalf("package description: %d %s", desc.Code, desc.Body)
	}

	f.pkg = jsonOf[providerPackage](t, desc)

	return f
}

func (f *providerFixture) fetch(t *testing.T, link string) *httptest.ResponseRecorder {
	t.Helper()

	return f.e.do(http.MethodGet, ticketPathOf(t, link), nil)
}

func TestProviderVersions(t *testing.T) {
	f := newProviderFixture(t)

	w := f.e.do(http.MethodGet, "/repos/myrepo/aws/versions", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	got := jsonOf[struct {
		Versions []struct {
			Version   string   `json:"version"`
			Protocols []string `json:"protocols"`
			Platforms []struct {
				OS   string `json:"os"`
				Arch string `json:"arch"`
			} `json:"platforms"`
		} `json:"versions"`
	}](t, w)

	if len(got.Versions) != 2 || got.Versions[0].Version != "1.2.3" || got.Versions[1].Version != "1.3.0-beta.1" {
		t.Fatalf("versions = %+v", got.Versions)
	}

	if strings.Join(got.Versions[0].Protocols, ",") != "5.0,6.0" || len(got.Versions[0].Platforms) != 2 {
		t.Fatalf("1.2.3 = %+v", got.Versions[0])
	}

	if strings.Join(got.Versions[1].Protocols, ",") != "5.0" {
		t.Fatalf("a provider with no declared protocols must default to 5.0, got %v", got.Versions[1].Protocols)
	}
}

func TestProviderPackageDescription(t *testing.T) {
	f := newProviderFixture(t)
	pkg := f.pkg

	if pkg.Filename != "terraform-provider-aws_1.2.3_linux_amd64.zip" || pkg.Shasum != hexSHA(f.linux) || pkg.OS != "linux" ||
		pkg.Arch != "amd64" {
		t.Fatalf("package = %+v", pkg)
	}

	if !strings.HasSuffix(pkg.SumsURL, "/terraform-provider-aws_1.2.3_SHA256SUMS") ||
		!strings.HasSuffix(pkg.SigURL, "/terraform-provider-aws_1.2.3_SHA256SUMS.sig") {
		t.Fatalf("urls = %q %q", pkg.SumsURL, pkg.SigURL)
	}

	if len(pkg.SigningKeys.Keys) != 1 || len(pkg.SigningKeys.Keys[0].KeyID) != 16 ||
		!strings.HasSuffix(strings.ToUpper(f.signer.KeyFingerprint()), pkg.SigningKeys.Keys[0].KeyID) {
		t.Fatalf("signing keys = %+v", pkg.SigningKeys)
	}

	if got := f.e.db.provs[0].DownloadCount; got != 1 {
		t.Fatalf("download count = %d", got)
	}
}

func TestProviderZipDownload(t *testing.T) {
	f := newProviderFixture(t)

	if w := f.fetch(t, f.pkg.DownloadURL); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), f.linux) {
		t.Fatalf("zip download: %d, %d bytes", w.Code, w.Body.Len())
	}
}

func TestProviderChecksumsListEveryPlatformOfTheVersion(t *testing.T) {
	f := newProviderFixture(t)

	w := f.fetch(t, f.pkg.SumsURL)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}

	want := hexSHA(f.darwin) + "  terraform-provider-aws_1.2.3_darwin_arm64.zip\n" +
		hexSHA(f.linux) + "  terraform-provider-aws_1.2.3_linux_amd64.zip\n"
	if w.Body.String() != want {
		t.Fatalf("SHA256SUMS =\n%s\nwant\n%s", w.Body, want)
	}
}

func TestProviderSignatureIsBinaryAndVerifies(t *testing.T) {
	f := newProviderFixture(t)
	sums := f.fetch(t, f.pkg.SumsURL).Body.Bytes()

	w := f.fetch(t, f.pkg.SigURL)
	if armored := bytes.Contains(w.Body.Bytes(), []byte("BEGIN PGP")); w.Code != http.StatusOK || armored {
		t.Fatalf("status %d, armored=%v", w.Code, armored)
	}

	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(f.pkg.SigningKeys.Keys[0].Armor))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := openpgp.CheckDetachedSignature(ring, bytes.NewReader(sums), w.Body, nil); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
}

// TestProviderSigningFollowsKeyRotation: nothing signed is stored, so rotating
// the repository's key needs no re-signing step. The package document and the
// signature are both made from the key the repository has when they are
// requested, and what `terraform init` checks (the served signing key against
// the served signature of the served SHA256SUMS) holds before and after.
func TestProviderSigningFollowsKeyRotation(t *testing.T) {
	f := newProviderFixture(t)

	verify := func(t *testing.T, signer *gpg.LocalSigner) {
		t.Helper()

		desc := f.e.do(http.MethodGet, "/repos/myrepo/aws/1.2.3/download/linux/amd64", nil)
		if desc.Code != http.StatusOK {
			t.Fatalf("package description: %d %s", desc.Code, desc.Body)
		}

		pkg := jsonOf[providerPackage](t, desc)
		if len(pkg.SigningKeys.Keys) != 1 || !strings.HasSuffix(strings.ToUpper(signer.KeyFingerprint()), pkg.SigningKeys.Keys[0].KeyID) {
			t.Fatalf("signing keys = %+v, want only the key %s", pkg.SigningKeys, signer.KeyFingerprint())
		}

		ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pkg.SigningKeys.Keys[0].Armor))
		if err != nil {
			t.Fatal(err)
		}

		sums, sig := f.fetch(t, pkg.SumsURL), f.fetch(t, pkg.SigURL)
		if sums.Code != http.StatusOK || sig.Code != http.StatusOK {
			t.Fatalf("sums %d, signature %d", sums.Code, sig.Code)
		}

		signedBy, err := openpgp.CheckDetachedSignature(ring, bytes.NewReader(sums.Body.Bytes()), sig.Body, nil)
		if err != nil {
			t.Fatalf("the served signature does not verify against the served key: %v", err)
		}

		if got := strings.ToUpper(hex.EncodeToString(signedBy.PrimaryKey.Fingerprint)); got != strings.ToUpper(signer.KeyFingerprint()) {
			t.Fatalf("signed by %s, want %s", got, signer.KeyFingerprint())
		}
	}

	verify(t, f.signer)

	rotated := newSigner(t)
	f.e.h.WithSigner(signerResolver{rotated})

	verify(t, rotated)
}

func TestProviderChecksumsTrackNewPlatforms(t *testing.T) {
	f := newProviderFixture(t)

	extra := providerZip(t, "aws", "1.2.3", [2]string{"EXTRA", "x"})
	if w := f.e.putProvider("aws", "1.2.3", "windows", "amd64", extra, ""); w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}

	if got := f.fetch(
		t,
		f.pkg.SumsURL,
	).Body.String(); !strings.Contains(
		got,
		hexSHA(extra)+"  terraform-provider-aws_1.2.3_windows_amd64.zip",
	) {
		t.Fatalf("SHA256SUMS lacks the new platform:\n%s", got)
	}
}

func TestProviderUnknownCoordinates(t *testing.T) {
	f := newProviderFixture(t)

	for _, path := range []string{
		"/repos/myrepo/aws/1.2.3/download/freebsd/amd64",
		"/repos/myrepo/aws/9.9.9/download/linux/amd64",
		"/repos/myrepo/gcp/versions",
		"/repos/myrepo/aws/1.2.3/download/Linux/amd64",
		"/repos/myrepo/aws/latest/download/linux/amd64",
	} {
		if w := f.e.do(http.MethodGet, path, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}
}

func TestProviderWithoutSigningKey(t *testing.T) {
	e := newEnv(t)

	if w := e.putProvider("aws", "1.0.0", "linux", "amd64", providerZip(t, "aws", "1.0.0"), ""); w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}

	for _, tc := range []struct {
		name     string
		resolver gpg.Resolver
	}{
		{"no resolver", nil},
		{"unsigned repository", signerResolver{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e.h.signers = tc.resolver

			w := e.do(http.MethodGet, "/repos/myrepo/aws/1.0.0/download/linux/amd64", nil)
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "no GPG signing key") {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
		})
	}
}

func TestProviderUploadErrors(t *testing.T) {
	e := newEnv(t)
	good := providerZip(t, "aws", "1.0.0")

	if w := e.putProvider("aws", "1.0.0", "linux", "amd64", good, "?protocols=6.0"); w.Code != http.StatusCreated {
		t.Fatalf("seed upload: %d %s", w.Code, w.Body)
	}

	for _, tc := range []struct {
		name                   string
		typ, version, os, arch string
		body                   []byte
		query                  string
		headers                []string
		want                   int
	}{
		{name: "same platform again", typ: "aws", version: "1.0.0", os: "linux", arch: "amd64", body: good, want: http.StatusConflict},
		{name: "explicit protocols differ from the version's", typ: "aws", version: "1.0.0", os: "linux", arch: "arm64", body: good, query: "?protocols=5.0", want: http.StatusConflict},
		{name: "bad protocols", typ: "aws", version: "1.0.0", os: "linux", arch: "arm64", body: good, query: "?protocols=five", want: http.StatusBadRequest},
		{name: "checksum mismatch", typ: "aws", version: "1.0.0", os: "linux", arch: "arm64", body: good, headers: []string{checksumHeader, strings.Repeat("0", 64)}, want: http.StatusBadRequest},
		{name: "checksum malformed", typ: "aws", version: "1.0.0", os: "linux", arch: "arm64", body: good, headers: []string{checksumHeader, "abc"}, want: http.StatusBadRequest},
		{name: "bad os", typ: "aws", version: "1.0.0", os: "Linux", arch: "amd64", body: good, want: http.StatusBadRequest},
		{name: "bad version", typ: "aws", version: "v1.0.0", os: "linux", arch: "amd64", body: good, want: http.StatusBadRequest},
		{name: "reserved type", typ: "packages", version: "1.0.0", os: "linux", arch: "amd64", body: good, want: http.StatusBadRequest},
		{name: "empty body", typ: "aws", version: "1.0.1", os: "linux", arch: "amd64", body: nil, want: http.StatusBadRequest},
		{name: "not a zip", typ: "aws", version: "1.0.1", os: "linux", arch: "amd64", body: []byte("nope"), want: http.StatusBadRequest},
		{name: "binary of another version", typ: "aws", version: "1.0.1", os: "linux", arch: "amd64", body: good, want: http.StatusUnprocessableEntity},
		{name: "tar.gz", typ: "aws", version: "1.0.1", os: "linux", arch: "amd64", body: tarGzOf(t, tarEntry{name: "x", body: "x"}), want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, blobs, used := len(e.db.provs), len(e.bs.Blobs), e.db.used

			w := e.putProvider(tc.typ, tc.version, tc.os, tc.arch, tc.body, tc.query, tc.headers...)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}

			if len(e.db.provs) != rows || len(e.bs.Blobs) != blobs || e.db.used != used {
				t.Fatalf("rejected upload changed state")
			}
		})
	}

	t.Run("a platform without a stated protocol inherits the version's", func(t *testing.T) {
		w := e.putProvider("aws", "1.0.0", "darwin", "amd64", good, "")
		if w.Code != http.StatusCreated {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}

		if got := jsonOf[map[string]any](t, w)["protocols"]; len(got.([]any)) != 1 || got.([]any)[0] != "6.0" {
			t.Fatalf("protocols = %v, want [6.0]", got)
		}
	})

	t.Run("the manifest in the zip states the protocols", func(t *testing.T) {
		zipWithManifest := providerZip(t, "gcp", "2.0.0", [2]string{manifestName, `{"version":1,"metadata":{"protocol_versions":["6.0"]}}`})

		w := e.putProvider("gcp", "2.0.0", "linux", "amd64", zipWithManifest, "")
		if w.Code != http.StatusCreated {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}

		if got := e.db.provs[len(e.db.provs)-1].Protocols; len(got) != 1 || got[0] != "6.0" {
			t.Fatalf("protocols = %v", got)
		}
	})

	t.Run("quota", func(t *testing.T) {
		e.db.limit = e.db.used
		defer func() { e.db.limit = 0 }()

		if w := e.putProvider("aws", "1.0.0", "windows", "amd64", good, ""); w.Code != http.StatusInsufficientStorage {
			t.Fatalf("status %d, want 507", w.Code)
		}
	})
}

func TestDeleteProvider(t *testing.T) {
	e := newEnv(t)

	for _, p := range [][2]string{{"linux", "amd64"}, {"darwin", "arm64"}} {
		if w := e.putProvider(
			"aws",
			"1.0.0",
			p[0],
			p[1],
			providerZip(t, "aws", "1.0.0", [2]string{p[0], "x"}),
			"",
		); w.Code != http.StatusCreated {
			t.Fatalf("upload: %d %s", w.Code, w.Body)
		}
	}

	if w := e.putProvider("aws", "2.0.0", "linux", "amd64", providerZip(t, "aws", "2.0.0"), ""); w.Code != http.StatusCreated {
		t.Fatalf("upload: %d", w.Code)
	}

	w := e.do(http.MethodDelete, "/repos/myrepo/_api/providers/aws/1.0.0/darwin/arm64", nil)
	if w.Code != http.StatusOK || len(e.db.provs) != 2 || e.db.artifacts != 2 {
		t.Fatalf("platform delete: %d, rows=%d artifacts=%d", w.Code, len(e.db.provs), e.db.artifacts)
	}

	if w := e.do(http.MethodDelete, "/repos/myrepo/_api/providers/aws/1.0.0/darwin/arm64", nil); w.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", w.Code)
	}

	w = e.do(http.MethodDelete, "/repos/myrepo/_api/providers/aws/1.0.0", nil)
	if w.Code != http.StatusOK || jsonOf[map[string]int](t, w)["deleted"] != 1 || len(e.db.provs) != 1 {
		t.Fatalf("version delete: %d %s, rows=%d", w.Code, w.Body, len(e.db.provs))
	}

	if w := e.do(http.MethodDelete, "/repos/myrepo/_api/providers/aws/1.0.0", nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete of a gone version = %d, want 404", w.Code)
	}

	e.db.Repo.ImmutableReleases = true

	if w := e.do(http.MethodDelete, "/repos/myrepo/_api/providers/aws/2.0.0", nil); w.Code != http.StatusConflict {
		t.Fatalf("immutable release delete = %d, want 409", w.Code)
	}
}

func TestQuarantineBlocksMinting(t *testing.T) {
	e := newEnv(t)
	e.h.WithSigner(signerResolver{newSigner(t)})

	if w := e.putModule("vpc", "aws", "1.0.0", moduleZip(t)); w.Code != http.StatusCreated {
		t.Fatalf("upload: %d", w.Code)
	}

	if w := e.putProvider("aws", "1.0.0", "linux", "amd64", providerZip(t, "aws", "1.0.0"), ""); w.Code != http.StatusCreated {
		t.Fatalf("upload: %d", w.Code)
	}

	for name, key := range map[string]string{
		"module by file name":   "vpc-aws-1.0.0.zip",
		"module by coordinates": "modules/vpc/aws/1.0.0",
	} {
		t.Run(name, func(t *testing.T) {
			e.db.Keys[key] = true
			defer delete(e.db.Keys, key)

			w := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/1.0.0/download", nil)
			if w.Code != http.StatusForbidden || w.Header().Get("X-Terraform-Get") != "" {
				t.Fatalf("status %d, X-Terraform-Get %q", w.Code, w.Header().Get("X-Terraform-Get"))
			}

			if got := e.db.mods[0].DownloadCount; got != 0 {
				t.Fatalf("a refused download was counted: %d", got)
			}

			if w := e.do(http.MethodGet, "/repos/myrepo/vpc/aws/versions", nil); w.Code != http.StatusOK {
				t.Fatalf("the version list must stay readable, got %d", w.Code)
			}
		})
	}

	for name, key := range map[string]string{
		"provider by file name":   "terraform-provider-aws_1.0.0_linux_amd64.zip",
		"provider by coordinates": "providers/aws/1.0.0/linux/amd64",
	} {
		t.Run(name, func(t *testing.T) {
			e.db.Keys[key] = true
			defer delete(e.db.Keys, key)

			w := e.do(http.MethodGet, "/repos/myrepo/aws/1.0.0/download/linux/amd64", nil)
			if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "download_url") {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
		})
	}
}

// TestUploadHonorsImportCreatedAt: an upload carrying an origin creation time
// (as importmeta resolves it for a manage principal) stores it as created_at;
// an ordinary upload leaves the database to stamp now.
func TestUploadHonorsImportCreatedAt(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{"imported", formattest.Origin()},
		{"normal upload", time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)

			w := formattest.Do(formattest.AtOrigin(e.h.RegisterRepoRoutes, tc.at),
				formattest.Request(http.MethodPut, "/repos/myrepo/_api/modules/vpc/aws/1.0.0", bytes.NewReader(moduleZip(t))))
			if w.Code != http.StatusCreated {
				t.Fatalf("module upload: %d %s", w.Code, w.Body)
			}

			w = formattest.Do(
				formattest.AtOrigin(e.h.RegisterRepoRoutes, tc.at),
				formattest.Request(
					http.MethodPut,
					"/repos/myrepo/_api/providers/aws/1.0.0/linux/amd64",
					bytes.NewReader(providerZip(t, "aws", "1.0.0")),
				),
			)
			if w.Code != http.StatusCreated {
				t.Fatalf("provider upload: %d %s", w.Code, w.Body)
			}

			for _, got := range []time.Time{e.db.mods[0].CreatedAt, e.db.provs[0].CreatedAt} {
				if got.Equal(formattest.Origin()) != !tc.at.IsZero() {
					t.Fatalf("created_at = %v for an upload at origin %v", got, tc.at)
				}
			}
		})
	}
}
