// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/pkg/logger"
)

type webhooksMockDB struct {
	database.BaseMockDB

	stored *models.Webhook
}

func (db *webhooksMockDB) CreateWebhook(_ context.Context, wh *models.Webhook) error {
	wh.ID = "wh-1"
	db.stored = wh

	return nil
}

func (db *webhooksMockDB) GetWebhook(_ context.Context, id string) (*models.Webhook, error) {
	if db.stored != nil && db.stored.ID == id {
		cp := *db.stored
		return &cp, nil
	}

	return nil, nil
}

func (db *webhooksMockDB) UpdateWebhook(_ context.Context, wh *models.Webhook) error {
	db.stored = wh
	return nil
}

func newWebhooksTestRouter(t *testing.T, db *webhooksMockDB) (http.Handler, *http.Request) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	h := NewWebhooksHandler(db, log, auth.NewEvaluator(db, log, false), nil)
	r := chi.NewRouter()
	h.RegisterAuthed(r)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

	return r, req
}

func doWebhookRequest(t *testing.T, r http.Handler, base *http.Request, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(base.Context(), method, path, strings.NewReader(body))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	return rw
}

func TestWebhookCreateNeverReturnsSecretAndEncryptsIt(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "true")

	db := &webhooksMockDB{}
	r, base := newWebhooksTestRouter(t, db)

	rw := doWebhookRequest(t, r, base, http.MethodPost, "/api/v1/webhooks",
		`{"name":"ci","url":"http://127.0.0.1:9/hook","secret":"s3cret","events":["artifact.uploaded","artifact.uploaded"],"ssl_verify":false,"max_retries":99,"timeout_seconds":0}`)
	if rw.Code != http.StatusBadRequest && rw.Code != http.StatusCreated {
		t.Fatalf("unexpected status %d: %s", rw.Code, rw.Body.String())
	}

	if rw.Code == http.StatusBadRequest {
		t.Fatalf("create rejected: %s", rw.Body.String())
	}

	if strings.Contains(rw.Body.String(), "s3cret") || strings.Contains(rw.Body.String(), "secret_enc") || strings.Contains(strings.ToLower(rw.Body.String()), "secretenc") {
		t.Fatalf("response leaks secret: %s", rw.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp["id"] != "wh-1" || resp["has_secret"] != true || resp["ssl_verify"] != false || resp["enabled"] != true {
		t.Fatalf("unexpected response: %v", resp)
	}

	if resp["max_retries"] != float64(maxWebhookRetries) || resp["timeout_seconds"] != float64(1) {
		t.Fatalf("numbers not clamped: %v", resp)
	}

	if events, _ := resp["events"].([]any); len(events) != 1 {
		t.Fatalf("events not de-duplicated: %v", resp["events"])
	}

	if db.stored.SecretEnc == "" {
		t.Fatal("secret not stored")
	}
}

func TestWebhookCreateValidation(t *testing.T) {
	db := &webhooksMockDB{}
	r, base := newWebhooksTestRouter(t, db)

	for name, body := range map[string]string{
		"no name":         `{"url":"https://example.com/h","events":["artifact.uploaded"]}`,
		"no url":          `{"name":"x","events":["artifact.uploaded"]}`,
		"file scheme":     `{"name":"x","url":"file:///etc/passwd","events":["artifact.uploaded"]}`,
		"metadata":        `{"name":"x","url":"http://169.254.169.254/latest","events":["artifact.uploaded"]}`,
		"no events":       `{"name":"x","url":"https://93.184.216.34/h","events":[]}`,
		"unknown event":   `{"name":"x","url":"https://93.184.216.34/h","events":["push"]}`,
		"bad contenttype": `{"name":"x","url":"https://93.184.216.34/h","events":["artifact.uploaded"],"content_type":"text/plain"}`,
		"bad json":        `{`,
	} {
		rw := doWebhookRequest(t, r, base, http.MethodPost, "/api/v1/webhooks", body)
		if rw.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, body = %s", name, rw.Code, rw.Body.String())
		}
	}

	if db.stored != nil {
		t.Fatal("invalid webhook was stored")
	}
}

func TestWebhookCreateRejectsOversizedBody(t *testing.T) {
	db := &webhooksMockDB{}
	r, base := newWebhooksTestRouter(t, db)

	body := `{"name":"` + strings.Repeat("a", maxWebhookBodyBytes) + `"}`

	rw := doWebhookRequest(t, r, base, http.MethodPost, "/api/v1/webhooks", body)
	if rw.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", rw.Code, rw.Body.String())
	}
}

func TestWebhookUpdateHonoursSSLVerifyAndSecretIsKept(t *testing.T) {
	db := &webhooksMockDB{stored: &models.Webhook{
		ID: "wh-1", Name: "ci", URL: "https://93.184.216.34/h", SecretEnc: "stored-enc",
		Enabled: true, Events: []string{"artifact.uploaded"}, ContentType: "application/json",
		SSLVerify: true, MaxRetries: 3, TimeoutSeconds: 30,
	}}
	r, base := newWebhooksTestRouter(t, db)

	rw := doWebhookRequest(t, r, base, http.MethodPut, "/api/v1/webhooks/wh-1", `{"ssl_verify":false,"enabled":false,"timeout_seconds":1000}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rw.Code, rw.Body.String())
	}

	if db.stored.SSLVerify || db.stored.Enabled || db.stored.SecretEnc != "stored-enc" || db.stored.TimeoutSeconds != 30 {
		t.Fatalf("unexpected stored webhook: %+v", db.stored)
	}

	rw = doWebhookRequest(t, r, base, http.MethodGet, "/api/v1/webhooks/wh-1", "")
	if strings.Contains(rw.Body.String(), "stored-enc") || !strings.Contains(rw.Body.String(), `"has_secret":true`) {
		t.Fatalf("GET leaks or hides secret state: %s", rw.Body.String())
	}
}

func TestWebhookNonAdminForbidden(t *testing.T) {
	db := &webhooksMockDB{}
	r, base := newWebhooksTestRouter(t, db)
	base = base.WithContext(middleware.WithPrincipal(base.Context(), &auth.Principal{IsAnonymous: true}))

	rw := doWebhookRequest(t, r, base, http.MethodGet, "/api/v1/webhooks", "")
	if rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rw.Code)
	}
}
