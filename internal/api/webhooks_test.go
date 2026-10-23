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
	"github.com/openartifact/openartifact/internal/webhook"
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

func (db *webhooksMockDB) CreateWebhookDelivery(_ context.Context, d *models.WebhookDelivery) error {
	d.ID = "delivery-1"
	return nil
}

func (db *webhooksMockDB) UpdateWebhookDelivery(context.Context, *models.WebhookDelivery) error {
	return nil
}

func newWebhooksTestRouter(t *testing.T, db *webhooksMockDB) (http.Handler, *http.Request) {
	t.Helper()

	return newWebhooksTestRouterWith(t, db, nil)
}

func newWebhooksTestRouterWith(t *testing.T, db *webhooksMockDB, dispatcher *webhook.Dispatcher) (http.Handler, *http.Request) {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	h := NewWebhooksHandler(db, log, auth.NewEvaluator(db, log, false), dispatcher)
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
		"unknown format":  `{"name":"x","url":"https://93.184.216.34/h","events":["artifact.uploaded"],"payload_format":"xml"}`,
		"downloaded":      `{"name":"x","url":"https://93.184.216.34/h","events":["artifact.downloaded"]}`,
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

func TestWebhookPayloadFormat(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "true")

	create := func(t *testing.T, body string) (*webhooksMockDB, map[string]any) {
		t.Helper()

		db := &webhooksMockDB{}
		r, base := newWebhooksTestRouter(t, db)

		rw := doWebhookRequest(t, r, base, http.MethodPost, "/api/v1/webhooks", body)
		if rw.Code != http.StatusCreated {
			t.Fatalf("status = %d: %s", rw.Code, rw.Body.String())
		}

		var resp map[string]any
		if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}

		return db, resp
	}

	t.Run("defaults to openartifact", func(t *testing.T) {
		db, resp := create(t, `{"name":"x","url":"http://127.0.0.1:9/h","events":["artifact.uploaded"]}`)
		if db.stored.PayloadFormat != "openartifact" || resp["payload_format"] != "openartifact" {
			t.Fatalf("stored %q, response %v", db.stored.PayloadFormat, resp["payload_format"])
		}
	})

	for _, format := range webhook.PayloadFormats() {
		t.Run("create "+format, func(t *testing.T) {
			db, resp := create(t, `{"name":"x","url":"http://127.0.0.1:9/h","events":["artifact.uploaded"],"payload_format":"`+format+`"}`)
			if db.stored.PayloadFormat != format || resp["payload_format"] != format {
				t.Fatalf("stored %q, response %v", db.stored.PayloadFormat, resp["payload_format"])
			}
		})
	}

	t.Run("update changes and validates", func(t *testing.T) {
		db := &webhooksMockDB{stored: &models.Webhook{
			ID: "wh-1", Name: "ci", URL: "https://93.184.216.34/h", Enabled: true, Events: []string{"artifact.uploaded"},
			ContentType: "application/json", PayloadFormat: "openartifact", SSLVerify: true, MaxRetries: 3, TimeoutSeconds: 30,
		}}
		r, base := newWebhooksTestRouter(t, db)

		rw := doWebhookRequest(t, r, base, http.MethodPut, "/api/v1/webhooks/wh-1", `{"payload_format":"nope"}`)
		if rw.Code != http.StatusBadRequest || db.stored.PayloadFormat != "openartifact" {
			t.Fatalf("invalid format: status %d, stored %q", rw.Code, db.stored.PayloadFormat)
		}

		rw = doWebhookRequest(t, r, base, http.MethodPut, "/api/v1/webhooks/wh-1", `{"payload_format":"teams"}`)
		if rw.Code != http.StatusOK || db.stored.PayloadFormat != "teams" {
			t.Fatalf("status %d, stored %q: %s", rw.Code, db.stored.PayloadFormat, rw.Body.String())
		}

		// An update that does not mention the format keeps it.
		rw = doWebhookRequest(t, r, base, http.MethodPut, "/api/v1/webhooks/wh-1", `{"enabled":false}`)
		if rw.Code != http.StatusOK || db.stored.PayloadFormat != "teams" {
			t.Fatalf("format not kept: status %d, stored %q", rw.Code, db.stored.PayloadFormat)
		}
	})

	t.Run("rows predating the column read as openartifact", func(t *testing.T) {
		db := &webhooksMockDB{stored: &models.Webhook{ID: "wh-1", Name: "old", URL: "https://93.184.216.34/h"}}
		r, base := newWebhooksTestRouter(t, db)

		rw := doWebhookRequest(t, r, base, http.MethodGet, "/api/v1/webhooks/wh-1", "")
		if !strings.Contains(rw.Body.String(), `"payload_format":"openartifact"`) {
			t.Fatalf("body = %s", rw.Body.String())
		}
	})
}

// TestWebhookTestDeliveryUsesPayloadFormat drives POST /webhooks/{id}/test
// through a real dispatcher and checks the request the receiver gets.
func TestWebhookTestDeliveryUsesPayloadFormat(t *testing.T) {
	t.Setenv(netutil.EnvAllowPrivate, "true")

	type received struct {
		contentType string
		body        map[string]any
	}

	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- received{r.Header.Get("Content-Type"), body}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	for format, check := range map[string]func(received) bool{
		"openartifact": func(r received) bool { return r.contentType == "application/json" && r.body["event"] == "webhook.test" },
		"cloudevents": func(r received) bool {
			return r.contentType == "application/cloudevents+json" && r.body["specversion"] == "1.0" &&
				r.body["type"] == "io.openartifact.webhook.test" && r.body["id"] == "delivery-1"
		},
		"slack":   func(r received) bool { return r.body["blocks"] != nil && r.body["text"] != nil },
		"teams":   func(r received) bool { return r.body["type"] == "message" && r.body["attachments"] != nil },
		"discord": func(r received) bool { return r.body["embeds"] != nil },
	} {
		t.Run(format, func(t *testing.T) {
			db := &webhooksMockDB{stored: &models.Webhook{
				ID: "wh-1", Name: "t", URL: srv.URL, Enabled: true, Events: []string{"artifact.uploaded"},
				ContentType: "application/json", PayloadFormat: format, SSLVerify: true, MaxRetries: 1, TimeoutSeconds: 5,
			}}

			d := webhook.NewDispatcher(db, log)
			d.Start()
			t.Cleanup(d.Stop)

			r, base := newWebhooksTestRouterWith(t, db, d)

			rw := doWebhookRequest(t, r, base, http.MethodPost, "/api/v1/webhooks/wh-1/test", "")
			if rw.Code != http.StatusAccepted {
				t.Fatalf("status = %d: %s", rw.Code, rw.Body.String())
			}

			rec := <-got
			if !check(rec) {
				t.Fatalf("unexpected %s request: content type %q, body %v", format, rec.contentType, rec.body)
			}
		})
	}
}
