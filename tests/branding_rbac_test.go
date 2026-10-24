// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/branding"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	brAdmin = "00000000-0000-4000-8000-0000000000a1"
	brUser  = "00000000-0000-4000-8000-0000000000a2"
)

// newBrandingServer wires the branding routes the way cmd/server does: public
// routes on the bare router, admin routes behind the real RBAC middleware.
func newBrandingServer(t *testing.T) (http.Handler, map[string]*http.Cookie) {
	t.Helper()

	f := newPZFixture(t)
	log, err := logger.New("error", "json", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}

	evaluator := auth.NewEvaluator(f.db, log, false)
	mgr := branding.NewManager(f.db, branding.Defaults{ProductName: "OpenArtifact", SourceURL: branding.DefaultSourceURL}, log)
	h := api.NewBrandingHandler(mgr, log, evaluator)

	authed := chi.NewRouter()
	authed.Use(middleware.RBACMiddleware(middleware.RBACConfig{}, f.db, evaluator, log))
	h.RegisterAuthed(authed)

	router := chi.NewRouter()
	h.RegisterPublic(router)
	router.Mount("/", authed)

	return router, f.cookie
}

func brandingDo(t *testing.T, router http.Handler, c *http.Cookie, method, path, ctype, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}

	if c != nil {
		req.AddCookie(c)
	}

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func TestBrandingRBAC_PublicVersusAdmin(t *testing.T) {
	router, cookies := newBrandingServer(t)
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>`

	// Public: no credentials needed, with or without a (non-admin) session.
	for _, c := range []*http.Cookie{nil, cookies[brUser], cookies[brAdmin]} {
		if rw := brandingDo(t, router, c, http.MethodGet, "/api/v1/branding", "", ""); rw.Code != http.StatusOK {
			t.Errorf("GET /api/v1/branding = %d", rw.Code)
		}
	}

	admin := []struct{ method, path, ctype, body string }{
		{http.MethodGet, "/api/v1/admin/branding", "", ""},
		{http.MethodPut, "/api/v1/admin/branding", "application/json", `{"product_name":"X"}`},
		{http.MethodDelete, "/api/v1/admin/branding", "", ""},
		{http.MethodPut, "/api/v1/admin/branding/favicon", "image/svg+xml", svg},
		{http.MethodDelete, "/api/v1/admin/branding/favicon", "", ""},
	}

	for _, c := range admin {
		if rw := brandingDo(t, router, nil, c.method, c.path, c.ctype, c.body); rw.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d, want 401", c.method, c.path, rw.Code)
		}

		if rw := brandingDo(t, router, cookies[brUser], c.method, c.path, c.ctype, c.body); rw.Code != http.StatusForbidden {
			t.Errorf("non-admin %s %s = %d, want 403", c.method, c.path, rw.Code)
		}
	}

	// A non-admin write attempt left nothing behind.
	if rw := brandingDo(t, router, nil, http.MethodGet, "/api/v1/branding", "", ""); strings.Contains(rw.Body.String(), `"X"`) {
		t.Errorf("forbidden write took effect: %s", rw.Body)
	}

	// The administrator can do all of it.
	adm := cookies[brAdmin]
	if rw := brandingDo(t, router, adm, http.MethodPut, "/api/v1/admin/branding", "application/json", `{"product_name":"Acme"}`); rw.Code != http.StatusOK {
		t.Fatalf("admin PUT = %d %s", rw.Code, rw.Body)
	}

	if rw := brandingDo(t, router, adm, http.MethodPut, "/api/v1/admin/branding/favicon", "image/svg+xml", svg); rw.Code != http.StatusOK {
		t.Fatalf("admin upload = %d %s", rw.Code, rw.Body)
	}

	if rw := brandingDo(t, router, nil, http.MethodGet, "/api/v1/branding/favicon", "", ""); rw.Code != http.StatusOK ||
		rw.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("anonymous favicon = %d %v", rw.Code, rw.Header())
	}

	if rw := brandingDo(t, router, nil, http.MethodGet, "/api/v1/branding", "", ""); !strings.Contains(rw.Body.String(), `"Acme"`) {
		t.Errorf("anonymous sees %s", rw.Body)
	}
}
