// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/openartifact/openartifact/internal/branding"
)

type fakeShellStore struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *fakeShellStore) GetSystemSetting(_ context.Context, k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.m[k], nil
}

func (s *fakeShellStore) SetSystemSetting(_ context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.m[k] = v

	return nil
}

func brandedSPA(t *testing.T) (http.Handler, *branding.Manager) {
	t.Helper()

	index, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}

	mgr := branding.NewManager(&fakeShellStore{m: map[string]string{}}, branding.Defaults{}, nil)

	return newSPAHandler(fstest.MapFS{"index.html": {Data: index}}, mgr), mgr
}

func setName(t *testing.T, mgr *branding.Manager, name string) {
	t.Helper()

	if _, err := mgr.Update(context.Background(), branding.Update{ProductName: branding.OptString{Set: true, Value: &name}}); err != nil {
		t.Fatal(err)
	}
}

// The real index.html must keep every marker the server patches: if someone
// edits the shell and a marker disappears the white-label title, favicon or
// iOS name would silently stay "OpenArtifact".
func TestBranding_RealIndexIsPatched(t *testing.T) {
	h, mgr := brandedSPA(t)

	setName(t, mgr, "Acme Hub")

	for _, p := range []string{"/", "/index.html", "/repos/x/browse", "/login"} {
		rw := do(h, http.MethodGet, p, nil)
		body := rw.Body.String()

		if rw.Code != http.StatusOK {
			t.Fatalf("%s = %d", p, rw.Code)
		}

		if !strings.Contains(body, "<title>Acme Hub</title>") ||
			!strings.Contains(body, `name="apple-mobile-web-app-title" content="Acme Hub"`) ||
			!strings.Contains(body, `id="oa-branding"`) {
			t.Errorf("%s not branded:\n%s", p, body)
		}

		if strings.Contains(body, "<title>OpenArtifact</title>") {
			t.Errorf("%s still has the default title", p)
		}

		if !strings.Contains(body, `rel="manifest" href="/manifest.webmanifest"`) {
			t.Errorf("%s lost the manifest link", p)
		}
	}
}

func TestBranding_ShellFollowsChanges(t *testing.T) {
	h, mgr := brandedSPA(t)

	etag := do(h, http.MethodGet, "/", nil).Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on index")
	}

	if rw := do(h, http.MethodGet, "/", map[string]string{"If-None-Match": etag}); rw.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d", rw.Code)
	}

	setName(t, mgr, "Second")

	rw := do(h, http.MethodGet, "/", map[string]string{"If-None-Match": etag})
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "<title>Second</title>") {
		t.Errorf("stale ETag after branding change: %d", rw.Code)
	}
}

func TestBranding_DynamicManifest(t *testing.T) {
	h, mgr := brandedSPA(t)
	setName(t, mgr, "Acme Hub")

	rw := do(h, http.MethodGet, "/manifest.webmanifest", nil)
	if rw.Code != http.StatusOK || !strings.HasPrefix(rw.Header().Get("Content-Type"), "application/manifest+json") {
		t.Fatalf("manifest = %d %q", rw.Code, rw.Header().Get("Content-Type"))
	}

	var m struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &m); err != nil || m.Name != "Acme Hub" || m.ShortName != "Acme Hub" {
		t.Errorf("manifest = %s (%v)", rw.Body, err)
	}

	if etag := rw.Header().Get("ETag"); etag == "" || do(h, http.MethodGet, "/manifest.webmanifest", map[string]string{"If-None-Match": etag}).Code != http.StatusNotModified {
		t.Error("manifest is not revalidatable")
	}
}

func TestBranding_NilSourceServesShellAsBuilt(t *testing.T) {
	h, _ := testSPA()

	rw := do(h, http.MethodGet, "/", nil)
	if rw.Code != http.StatusOK || strings.Contains(rw.Body.String(), "oa-branding") {
		t.Errorf("unbranded shell: %d", rw.Code)
	}
}
