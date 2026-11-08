// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/logger"
)

// declBackend is a stand-in for the server router: just enough of the REST API
// for the Webhook kind, recording what the engine sends.
type declBackend struct {
	mu       sync.Mutex
	webhooks []map[string]any
	posted   []map[string]any
	markers  int
	cookies  []string
	csrf     []string
	auth     []string
	gate     chan struct{} // when set, POST /api/v1/webhooks waits on it
	entered  chan struct{}
}

func (b *declBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.cookies = append(b.cookies, r.Header.Get("Cookie"))
	b.csrf = append(b.csrf, r.Header.Get("X-Csrf-Token"))
	b.auth = append(b.auth, r.Header.Get("Authorization"))
	b.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.URL.Path == "/api/v1/admin/declarative" && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `{"items":[]}`)
	case r.URL.Path == "/api/v1/admin/declarative" && r.Method == http.MethodPut:
		b.mu.Lock()
		b.markers++
		b.mu.Unlock()

		_, _ = io.WriteString(w, `{"items":[]}`)
	case r.URL.Path == "/api/v1/repositories":
		_, _ = io.WriteString(w, `{"total":0,"repositories":[]}`)
	case r.URL.Path == "/api/v1/webhooks" && r.Method == http.MethodGet:
		b.mu.Lock()
		defer b.mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{"webhooks": b.webhooks})
	case r.URL.Path == "/api/v1/webhooks" && r.Method == http.MethodPost:
		if b.gate != nil {
			b.entered <- struct{}{}
			<-b.gate
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		b.mu.Lock()
		b.posted = append(b.posted, body)
		b.mu.Unlock()

		_, _ = io.WriteString(w, `{"id":"w1"}`)
	default:
		http.NotFound(w, r)
	}
}

const webhookManifest = `apiVersion: openartifact.io/v1
kind: Webhook
metadata:
  name: audit-sink
spec:
  url: https://hooks.example.com/oa
  events: [artifact.uploaded]
  secret:
    secretRef: {env: HOOK_SECRET}
`

func newDeclRouter(t *testing.T, backend http.Handler, admin bool) http.Handler {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p := &auth.Principal{IsLegacy: true}
			if !admin {
				p = &auth.Principal{IsAnonymous: true}
			}

			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), p)))
		})
	})
	// The server's own router is a chi router: nested requests must be routed by their own path.
	routed := chi.NewRouter()
	for _, path := range []string{"/api/v1/admin/declarative", "/api/v1/repositories", "/api/v1/webhooks"} {
		routed.Handle(path, backend)
	}

	NewAdminDeclarativeRunHandler(routed, log, nil).Register(r)

	// Mounted like the server's authenticated router: the route context then carries the outer path.
	root := chi.NewRouter()
	root.Mount("/", r)

	return root
}

func declPost(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(string(data)))
	req.Header.Set("Cookie", "oa_session=s3cret; oa_csrf=tok")
	req.Header.Set("X-Csrf-Token", "tok")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

type declRunBody struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Changes bool   `json:"changes"`
	Summary map[string]int
	Items   []struct {
		Action string `json:"action"`
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		Result string `json:"result"`
		Error  string `json:"error"`
		Chg    []struct {
			Path   string `json:"path"`
			Secret bool   `json:"secret"`
		} `json:"changes"`
	} `json:"items"`
	Secrets []struct {
		Env      string `json:"env"`
		Provided bool   `json:"provided"`
	} `json:"secrets"`
	Problems []struct {
		Line    int    `json:"line"`
		Object  string `json:"object"`
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"problems"`
}

func decodeRun(t *testing.T, rec *httptest.ResponseRecorder) declRunBody {
	t.Helper()

	var out declRunBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}

	return out
}

func TestDeclarativeDiffPlansWithoutChangingAnything(t *testing.T) {
	t.Parallel()

	b := &declBackend{}
	h := newDeclRouter(t, b, true)

	rec := declPost(t, h, "/api/v1/admin/declarative/diff", map[string]any{"manifest": webhookManifest})
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}

	out := decodeRun(t, rec)
	if !out.Changes || len(out.Items) != 1 || out.Items[0].Action != "create" || out.Items[0].Name != "audit-sink" {
		t.Fatalf("plan = %+v", out)
	}

	if len(out.Secrets) != 1 || out.Secrets[0].Env != "HOOK_SECRET" || out.Secrets[0].Provided {
		t.Fatalf("secrets = %+v, want HOOK_SECRET listed and not provided", out.Secrets)
	}

	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("response leaks a credential: %s", rec.Body)
	}

	if len(b.posted) != 0 || b.markers != 0 {
		t.Fatalf("diff changed the server: posted=%v markers=%d", b.posted, b.markers)
	}
}

func TestDeclarativeEngineRequestsCarryTheCallersCredentials(t *testing.T) {
	t.Parallel()

	b := &declBackend{}
	h := newDeclRouter(t, b, true)

	if rec := declPost(t, h, "/api/v1/admin/declarative/diff", map[string]any{"manifest": webhookManifest}); rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}

	if len(b.cookies) == 0 {
		t.Fatal("the engine made no request")
	}

	for i := range b.cookies {
		if b.cookies[i] != "oa_session=s3cret; oa_csrf=tok" || b.csrf[i] != "tok" {
			t.Fatalf("request %d: cookie=%q csrf=%q, want the caller's", i, b.cookies[i], b.csrf[i])
		}
	}
}

func TestDeclarativeApplySendsTheSuppliedSecretAndRecordsOwnership(t *testing.T) {
	t.Parallel()

	b := &declBackend{}
	h := newDeclRouter(t, b, true)

	rec := declPost(t, h, "/api/v1/admin/declarative/apply", map[string]any{
		"manifest": webhookManifest, "secrets": map[string]string{"HOOK_SECRET": "hunter2"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}

	out := decodeRun(t, rec)
	if len(out.Items) != 1 || out.Items[0].Result != "applied" {
		t.Fatalf("outcome = %+v", out.Items)
	}

	if len(b.posted) != 1 || b.posted[0]["secret"] != "hunter2" || b.posted[0]["name"] != "audit-sink" {
		t.Fatalf("created webhook body = %v", b.posted)
	}

	if b.markers != 1 {
		t.Fatalf("ownership markers written %d times, want 1", b.markers)
	}

	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("response echoes the secret: %s", rec.Body)
	}
}

func TestDeclarativeApplyRefusesWhenASecretIsMissing(t *testing.T) {
	t.Setenv("HOOK_SECRET", "from-the-server-environment")

	b := &declBackend{}
	h := newDeclRouter(t, b, true)

	rec := declPost(t, h, "/api/v1/admin/declarative/apply", map[string]any{"manifest": webhookManifest})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}

	if out := decodeRun(t, rec); out.Code != "secrets_missing" {
		t.Fatalf("code = %q, want secrets_missing (the server environment must never fill a secret in)", out.Code)
	}

	if len(b.posted) != 0 {
		t.Fatalf("a webhook was created despite the missing secret: %v", b.posted)
	}
}

func TestDeclarativeFileSecretsAreNotReadFromTheServer(t *testing.T) {
	t.Parallel()

	b := &declBackend{}
	h := newDeclRouter(t, b, true)
	manifest := strings.Replace(webhookManifest, "{env: HOOK_SECRET}", "{file: /etc/passwd}", 1)

	rec := declPost(t, h, "/api/v1/admin/declarative/apply", map[string]any{"manifest": manifest})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}

	out := decodeRun(t, rec)
	if out.Code != "secrets_missing" || !strings.Contains(out.Error, "file secretRef") || len(b.posted) != 0 {
		t.Fatalf("out=%+v posted=%v", out, b.posted)
	}
}

func TestDeclarativeInvalidManifestReportsEveryProblemWithItsLine(t *testing.T) {
	t.Parallel()

	b := &declBackend{}
	h := newDeclRouter(t, b, true)
	manifest := "apiVersion: openartifact.io/v1\nkind: Webhook\nmetadata:\n  name: x\nspec:\n  url: https://hooks.example.com\n  events: [nope.event]\n  bogus: 1\n"

	rec := declPost(t, h, "/api/v1/admin/declarative/diff", map[string]any{"manifest": manifest})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}

	for _, field := range []string{`"items":[]`, `"summary":{}`, `"changes":false`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Fatalf("a refusal must keep the shape of a plan, missing %s in %s", field, rec.Body)
		}
	}

	out := decodeRun(t, rec)
	if out.Code != "manifest_invalid" || len(out.Problems) < 2 {
		t.Fatalf("out = %+v, want every problem listed", out)
	}

	for _, p := range out.Problems {
		if p.Line == 0 || p.Message == "" {
			t.Fatalf("problem without a line or message: %+v", p)
		}
	}

	if len(b.cookies) != 0 {
		t.Fatalf("the server was queried for an invalid manifest: %d requests", len(b.cookies))
	}
}

func TestDeclarativeManifestWithoutDocumentsKeepsTheShapeOfAPlan(t *testing.T) {
	t.Parallel()

	h := newDeclRouter(t, &declBackend{}, true)

	for _, manifest := range []string{"# only a comment\n", "---\n"} {
		rec := declPost(t, h, "/api/v1/admin/declarative/diff", map[string]any{"manifest": manifest})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("manifest %q: code=%d body=%s", manifest, rec.Code, rec.Body)
		}

		for _, field := range []string{`"items":[]`, `"summary":{}`, `"changes":false`, `"secrets":[]`} {
			if !strings.Contains(rec.Body.String(), field) {
				t.Fatalf("manifest %q: missing %s in %s", manifest, field, rec.Body)
			}
		}

		out := decodeRun(t, rec)
		if out.Code != "manifest_invalid" || len(out.Problems) != 1 || out.Problems[0].Message == "" {
			t.Fatalf("manifest %q: out = %+v, want one problem", manifest, out)
		}
	}
}

func TestDeclarativeRequestValidation(t *testing.T) {
	t.Parallel()

	h := newDeclRouter(t, &declBackend{}, true)

	cases := []struct {
		name string
		body any
		want int
	}{
		{"missing manifest", map[string]any{}, http.StatusBadRequest},
		{"blank manifest", map[string]any{"manifest": "  \n"}, http.StatusBadRequest},
		{"unknown field", map[string]any{"manifest": webhookManifest, "prun": true}, http.StatusBadRequest},
		{"unknown kind", map[string]any{"manifest": webhookManifest, "kinds": []string{"Bogus"}}, http.StatusBadRequest},
		{"only comments", map[string]any{"manifest": "# nothing\n"}, http.StatusUnprocessableEntity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if rec := declPost(t, h, "/api/v1/admin/declarative/diff", tc.body); rec.Code != tc.want {
				t.Fatalf("code=%d want %d body=%s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestDeclarativeRunIsAdminOnly(t *testing.T) {
	t.Parallel()

	b := &declBackend{}
	h := newDeclRouter(t, b, false)

	for _, path := range []string{"/api/v1/admin/declarative/diff", "/api/v1/admin/declarative/apply"} {
		if rec := declPost(t, h, path, map[string]any{"manifest": webhookManifest}); rec.Code != http.StatusForbidden {
			t.Fatalf("%s: code=%d", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/admin/declarative/export", http.NoBody))

	if rec.Code != http.StatusForbidden || len(b.cookies) != 0 {
		t.Fatalf("export: code=%d, engine requests=%d", rec.Code, len(b.cookies))
	}
}

func TestDeclarativeApplyRefusesAConcurrentApply(t *testing.T) {
	t.Parallel()

	b := &declBackend{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	h := newDeclRouter(t, b, true)
	body := map[string]any{"manifest": webhookManifest, "secrets": map[string]string{"HOOK_SECRET": "x"}}

	done := make(chan int, 1)

	go func() { done <- declPost(t, h, "/api/v1/admin/declarative/apply", body).Code }()

	<-b.entered

	if rec := declPost(t, h, "/api/v1/admin/declarative/apply", body); rec.Code != http.StatusConflict {
		t.Fatalf("second apply: code=%d body=%s", rec.Code, rec.Body)
	}

	close(b.gate)

	if code := <-done; code != http.StatusOK {
		t.Fatalf("first apply: code=%d", code)
	}
}

func TestDeclarativeExportDownloadsYAML(t *testing.T) {
	t.Parallel()

	b := &declBackend{webhooks: []map[string]any{{
		"id": "w1", "name": "audit-sink", "url": "https://hooks.example.com/oa", "events": []string{"artifact.uploaded"},
		"content_type": "application/json", "payload_format": "openartifact", "ssl_verify": true,
		"max_retries": 3, "timeout_seconds": 10, "enabled": true, "has_secret": true,
	}}}
	h := newDeclRouter(t, b, true)

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/api/v1/admin/declarative/export?kind=Webhook",
		http.NoBody,
	)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}

	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".yaml") {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "kind: Webhook") || !strings.Contains(body, "name: audit-sink") || !strings.Contains(body, "secretRef") {
		t.Fatalf("export = %s", body)
	}

	bad := httptest.NewRecorder()
	h.ServeHTTP(
		bad,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/admin/declarative/export?kind=Bogus", http.NoBody),
	)

	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind: code=%d", bad.Code)
	}
}

// leakyBackend answers every request with a 500 whose body carries internal text.
type leakyBackend struct{}

func (leakyBackend) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "pq: relation secret_table does not exist at 10.0.0.7:5432", http.StatusInternalServerError)
}

func TestDeclarativeExportFailureDoesNotLeakUpstreamText(t *testing.T) {
	t.Parallel()

	h := newDeclRouter(t, leakyBackend{}, true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(
		context.Background(), http.MethodGet, "/api/v1/admin/declarative/export?kind=Webhook", http.NoBody))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}

	if strings.Contains(rec.Body.String(), "secret_table") || strings.Contains(rec.Body.String(), "10.0.0.7") {
		t.Fatalf("response leaks upstream text: %s", rec.Body)
	}
}
