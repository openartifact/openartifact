// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/branding"
	"github.com/openartifact/openartifact/internal/middleware"
)

const brandSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>`

func newBrandingRouter(t *testing.T) (http.Handler, *branding.Manager) {
	t.Helper()

	db := newProjectsMock()
	log := newFixTestLogger(t)
	mgr := branding.NewManager(db, branding.Defaults{ProductName: "OpenArtifact", SourceURL: branding.DefaultSourceURL}, log)
	h := NewBrandingHandler(mgr, log, auth.NewEvaluator(db, log, false))

	r := chi.NewRouter()
	h.RegisterPublic(r)
	h.RegisterAuthed(r)

	return r, mgr
}

func brandReq(t *testing.T, router http.Handler, p *auth.Principal, method, target, ctype string, body []byte, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, target, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}

	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}

	if p != nil {
		req = req.WithContext(middleware.WithPrincipal(req.Context(), p))
	}

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func TestBranding_PublicGetAnonymous(t *testing.T) {
	router, _ := newBrandingRouter(t)

	rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding", "", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rw.Code, rw.Body)
	}

	var got struct {
		ProductName string  `json:"product_name"`
		SourceURL   string  `json:"source_url"`
		Revision    string  `json:"revision"`
		Theme       *string `json:"theme"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil || got.ProductName != "OpenArtifact" || got.SourceURL != branding.DefaultSourceURL || got.Revision == "" {
		t.Fatalf("body = %s (%v)", rw.Body, err)
	}

	if ct := rw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}

	if cc := rw.Header().Get("Cache-Control"); !strings.Contains(cc, "public") {
		t.Errorf("anonymous Cache-Control = %q, want public", cc)
	}

	if v := rw.Header().Values("Vary"); !strings.Contains(strings.Join(v, ","), "Cookie") || !strings.Contains(strings.Join(v, ","), "Authorization") {
		t.Errorf("Vary = %v", v)
	}
}

func TestBranding_CacheControlPublicOnlyForAnonymous(t *testing.T) {
	router, _ := newBrandingRouter(t)

	for name, kv := range map[string][]string{
		"cookie":        {"Cookie", "oa_session=abc"},
		"authorization": {"Authorization", "Bearer x"},
	} {
		rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding", "", nil, kv...)
		if cc := rw.Header().Get("Cache-Control"); strings.Contains(cc, "public") || rw.Code != http.StatusOK {
			t.Errorf("%s: Cache-Control = %q status %d, must not be public", name, cc, rw.Code)
		}
	}
}

func TestBranding_ETagRevalidation(t *testing.T) {
	router, mgr := newBrandingRouter(t)

	first := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding", "", nil)
	etag := first.Header().Get("ETag")

	if etag == "" {
		t.Fatal("no ETag")
	}

	for _, inm := range []string{etag, "W/" + etag, `"nope", ` + etag, "*"} {
		rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding", "", nil, "If-None-Match", inm)
		if rw.Code != http.StatusNotModified || rw.Body.Len() != 0 || rw.Header().Get("ETag") != etag {
			t.Errorf("If-None-Match %q: status %d body %d bytes", inm, rw.Code, rw.Body.Len())
		}
	}

	if rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding", "", nil, "If-None-Match", `"stale"`); rw.Code != http.StatusOK {
		t.Errorf("stale validator = %d, want 200", rw.Code)
	}

	// After a change the old validator no longer matches.
	name := "Acme"
	if _, err := mgr.Update(context.Background(), branding.Update{ProductName: branding.OptString{Set: true, Value: &name}}); err != nil {
		t.Fatal(err)
	}

	rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding", "", nil, "If-None-Match", etag)
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "Acme") || rw.Header().Get("ETag") == etag {
		t.Errorf("after update: status %d etag %q body %s", rw.Code, rw.Header().Get("ETag"), rw.Body)
	}
}

func TestBranding_ImageServing(t *testing.T) {
	router, _ := newBrandingRouter(t)
	admin := testPrincipal(uAdmin1)

	for _, kind := range []string{"logo-light", "logo-dark", "favicon"} {
		if rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding/"+kind, "", nil); rw.Code != http.StatusNotFound {
			t.Errorf("%s without image = %d, want 404", kind, rw.Code)
		}
	}

	if rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding/evil", "", nil); rw.Code != http.StatusNotFound {
		t.Errorf("unknown asset = %d", rw.Code)
	}

	if rw := brandReq(t, router, admin, http.MethodPut, "/api/v1/admin/branding/logo-light", "image/svg+xml", []byte(brandSVG)); rw.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", rw.Code, rw.Body)
	}

	// Public, no credentials.
	rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding/logo-light", "", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("image = %d", rw.Code)
	}

	h := rw.Header()
	if h.Get("Content-Type") != "image/svg+xml" ||
		h.Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'" ||
		h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Cache-Control"), "public") {
		t.Errorf("headers = %v", h)
	}

	etag := h.Get("ETag")
	if rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding/logo-light", "", nil, "If-None-Match", etag); rw.Code != http.StatusNotModified ||
		rw.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("image revalidation = %d headers %v", rw.Code, rw.Header())
	}

	// Dark falls back to the light logo.
	if rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding/logo-dark", "", nil); rw.Code != http.StatusNotFound {
		t.Errorf("logo-dark endpoint serves only an explicit dark image, got %d", rw.Code)
	}
}

func TestBranding_AdminEndpointsRequireAdmin(t *testing.T) {
	router, mgr := newBrandingRouter(t)

	calls := []struct{ method, path, ctype, body string }{
		{http.MethodGet, "/api/v1/admin/branding", "", ""},
		{http.MethodPut, "/api/v1/admin/branding", "application/json", `{"product_name":"Hacked"}`},
		{http.MethodDelete, "/api/v1/admin/branding", "", ""},
		{http.MethodPut, "/api/v1/admin/branding/logo-light", "image/svg+xml", brandSVG},
		{http.MethodPut, "/api/v1/admin/branding/favicon", "image/svg+xml", brandSVG},
		{http.MethodDelete, "/api/v1/admin/branding/logo-light", "", ""},
	}

	for name, p := range map[string]*auth.Principal{
		"anonymous":      nil,
		"plain user":     testPrincipal(uPlain1),
		"project owner":  testPrincipal(uOwner1),
		"anonymous flag": {IsAnonymous: true},
	} {
		for _, c := range calls {
			rw := brandReq(t, router, p, c.method, c.path, c.ctype, []byte(c.body))
			if rw.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c.method, c.path, rw.Code)
			}
		}
	}

	if got := mgr.Snapshot(context.Background()).Public.ProductName; got != "OpenArtifact" {
		t.Errorf("a forbidden call changed the branding: %q", got)
	}
}

func TestBranding_AdminUpdateReset(t *testing.T) {
	router, _ := newBrandingRouter(t)
	admin := testPrincipal(uAdmin1)

	rw := brandReq(t, router, admin, http.MethodPut, "/api/v1/admin/branding", "application/json",
		[]byte(`{"product_name":"Acme","primary_color":"#0F766E","login_message":"Hello","support_url":"mailto:it@acme.test"}`))
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rw.Code, rw.Body)
	}

	var got struct {
		Effective struct {
			ProductName  string `json:"product_name"`
			PrimaryColor string `json:"primary_color"`
			SupportURL   string `json:"support_url"`
			SourceURL    string `json:"source_url"`
		} `json:"effective"`
		Defaults struct {
			ProductName string `json:"product_name"`
		} `json:"defaults"`
		Overrides map[string]bool `json:"overrides"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if got.Effective.ProductName != "Acme" || got.Effective.PrimaryColor != "#0f766e" || got.Effective.SupportURL != "mailto:it@acme.test" ||
		got.Defaults.ProductName != "OpenArtifact" || !got.Overrides["product_name"] || got.Overrides["source_url"] {
		t.Errorf("response = %s", rw.Body)
	}

	// The public document reflects it immediately.
	if pub := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding", "", nil); !strings.Contains(pub.Body.String(), `"product_name":"Acme"`) {
		t.Errorf("public = %s", pub.Body)
	}

	// Source link cannot be cleared: attribution must stay reachable.
	rw = brandReq(t, router, admin, http.MethodPut, "/api/v1/admin/branding", "application/json", []byte(`{"source_url":""}`))
	if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), "validation_failed") {
		t.Errorf("empty source = %d %s", rw.Code, rw.Body)
	}

	rw = brandReq(t, router, admin, http.MethodDelete, "/api/v1/admin/branding", "", nil)
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), `"product_name":"OpenArtifact"`) {
		t.Errorf("reset = %d %s", rw.Code, rw.Body)
	}
}

func TestBranding_AdminValidation(t *testing.T) {
	router, _ := newBrandingRouter(t)
	admin := testPrincipal(uAdmin1)

	tests := []struct {
		name, body string
		want       int
		code       string
	}{
		{"bad colour", `{"primary_color":"red"}`, http.StatusBadRequest, "validation_failed"},
		{"javascript support url", `{"support_url":"javascript:alert(1)"}`, http.StatusBadRequest, "validation_failed"},
		{"empty name", `{"product_name":""}`, http.StatusBadRequest, "validation_failed"},
		{"unknown field", `{"logo":"x"}`, http.StatusBadRequest, "bad_request"},
		{"not json", `nope`, http.StatusBadRequest, "bad_request"},
		{"wrong type", `{"product_name":1}`, http.StatusBadRequest, "bad_request"},
		{"body too large", `{"login_message":"` + strings.Repeat("a", 70<<10) + `"}`, http.StatusBadRequest, "bad_request"},
	}

	for _, tt := range tests {
		rw := brandReq(t, router, admin, http.MethodPut, "/api/v1/admin/branding", "application/json", []byte(tt.body))
		if rw.Code != tt.want || !strings.Contains(rw.Body.String(), tt.code) {
			t.Errorf("%s: %d %s", tt.name, rw.Code, rw.Body)
		}
	}
}

func TestBranding_ImageUpload(t *testing.T) {
	router, _ := newBrandingRouter(t)
	admin := testPrincipal(uAdmin1)
	put := func(asset, ctype string, body []byte) *httptest.ResponseRecorder {
		return brandReq(t, router, admin, http.MethodPut, "/api/v1/admin/branding/"+asset, ctype, body)
	}

	// A hostile SVG is sanitised or refused, never stored as-is.
	evil := `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><script>alert(2)</script><circle r="4"/></svg>`
	if rw := put("logo-light", "image/svg+xml", []byte(evil)); rw.Code != http.StatusOK {
		t.Fatalf("sanitisable svg = %d %s", rw.Code, rw.Body)
	}

	served := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding/logo-light", "", nil).Body.String()
	if strings.Contains(served, "script") || strings.Contains(served, "onload") || !strings.Contains(served, "<circle") {
		t.Errorf("served svg = %s", served)
	}

	for name, c := range map[string]struct {
		asset, ctype string
		body         []byte
		want         int
	}{
		"doctype svg":   {"logo-light", "image/svg+xml", []byte(`<!DOCTYPE svg [<!ENTITY x "y">]><svg xmlns="http://www.w3.org/2000/svg"/>`), http.StatusBadRequest},
		"html":          {"logo-light", "text/html", []byte("<html/>"), http.StatusBadRequest},
		"gif":           {"favicon", "image/gif", []byte("GIF89a"), http.StatusBadRequest},
		"type mismatch": {"favicon", "image/png", []byte(brandSVG), http.StatusBadRequest},
		"corrupt png":   {"favicon", "image/png", []byte("\x89PNG\r\n\x1a\nxxxx"), http.StatusBadRequest},
		"oversize":      {"logo-dark", "image/png", bytes.Repeat([]byte("a"), branding.MaxImageBytes+1), http.StatusRequestEntityTooLarge},
		"unknown asset": {"banner", "image/svg+xml", []byte(brandSVG), http.StatusNotFound},
		"empty":         {"favicon", "image/png", nil, http.StatusBadRequest},
	} {
		rw := put(c.asset, c.ctype, c.body)
		if rw.Code != c.want {
			t.Errorf("%s: %d %s, want %d", name, rw.Code, rw.Body, c.want)
		}
	}

	if rw := put("favicon", "image/png", nil); rw.Code == http.StatusOK {
		t.Error("empty body stored")
	}

	if rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding/favicon", "", nil); rw.Code != http.StatusNotFound {
		t.Errorf("rejected uploads left a favicon: %d", rw.Code)
	}

	if rw := brandReq(t, router, admin, http.MethodDelete, "/api/v1/admin/branding/logo-light", "", nil); rw.Code != http.StatusOK {
		t.Fatalf("delete image = %d", rw.Code)
	}

	if rw := brandReq(t, router, nil, http.MethodGet, "/api/v1/branding/logo-light", "", nil); rw.Code != http.StatusNotFound {
		t.Errorf("image still served after delete: %d", rw.Code)
	}
}
