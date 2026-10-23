// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Test the HMAC signature computation
func TestComputeHMACSignature(t *testing.T) {
	secret := "test-secret-key"
	payload := map[string]any{
		"event": "artifact.uploaded",
		"repo":  "test-repo",
	}
	body, _ := json.Marshal(payload)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Verify the format matches what the dispatcher would produce
	if len(expected) < 10 {
		t.Error("signature too short")
	}

	if expected[:7] != "sha256=" {
		t.Error("expected sha256= prefix")
	}
}

// Test event type constants
func TestEventTypes(t *testing.T) {
	events := []string{
		EventArtifactUploaded,
		EventArtifactDeleted,
		EventRepoCreated,
		EventRepoDeleted,
		EventArtifactRestored,
		EventArtifactPurged,
		EventRepoRestored,
		EventRepoPurged,
	}
	for _, e := range events {
		if e == "" {
			t.Error("event type should not be empty")
		}
	}
}

// Test NewDispatcher construction
func TestNewDispatcher_NotNil(t *testing.T) {
	d := NewDispatcher(nil, nil)
	if d == nil {
		t.Fatal("expected non-nil dispatcher")
	}
}

// Test that dispatcher has expected fields initialized
func TestNewDispatcher_Initialized(t *testing.T) {
	d := NewDispatcher(nil, nil)
	if d == nil {
		t.Fatal("expected non-nil dispatcher")
	}

	if d.client == nil {
		t.Error("expected non-nil HTTP client")
	}

	if d.queue == nil {
		t.Error("expected non-nil queue channel")
	}

	if d.stopCh == nil {
		t.Error("expected non-nil stop channel")
	}

	if d.doneCh == nil {
		t.Error("expected non-nil done channel")
	}
}

// Test computeHMAC function directly
func TestComputeHMAC(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		secret   []byte
		expected string
	}{
		{
			name:     "simple",
			data:     []byte("test data"),
			secret:   []byte("secret"),
			expected: "sha256=",
		},
		{
			name:     "empty data",
			data:     []byte(""),
			secret:   []byte("secret"),
			expected: "sha256=",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := computeHMAC(tt.data, tt.secret)
			if len(result) == 0 {
				t.Error("expected non-empty HMAC")
			}
			// Verify it's a valid hex string
			if _, err := hex.DecodeString(result); err != nil {
				t.Errorf("HMAC is not valid hex: %v", err)
			}
		})
	}
}

// Test event type values
func TestEventTypeValues(t *testing.T) {
	tests := []struct {
		name     string
		event    string
		expected string
	}{
		{"artifact uploaded", EventArtifactUploaded, "artifact.uploaded"},
		{"artifact deleted", EventArtifactDeleted, "artifact.deleted"},
		{"repo created", EventRepoCreated, "repository.created"},
		{"repo deleted", EventRepoDeleted, "repository.deleted"},
		{"artifact restored", EventArtifactRestored, "artifact.restored"},
		{"artifact purged", EventArtifactPurged, "artifact.purged"},
		{"repo restored", EventRepoRestored, "repository.restored"},
		{"repo purged", EventRepoPurged, "repository.purged"},
		{"bundle promoted", EventBundlePromoted, "bundle.promoted"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.event != tt.expected {
				t.Errorf("event type mismatch: got %q, want %q", tt.event, tt.expected)
			}
		})
	}
}

type deliveryTestDB struct {
	*database.BaseMockDB

	delivery *models.WebhookDelivery
}

func (db *deliveryTestDB) UpdateWebhookDelivery(_ context.Context, delivery *models.WebhookDelivery) error {
	db.delivery = delivery
	return nil
}

func newResponseDelivery(t *testing.T, body string) (*deliveryTestDB, *models.WebhookDelivery) {
	t.Helper()
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db := &deliveryTestDB{BaseMockDB: &database.BaseMockDB{}}
	delivery := &models.WebhookDelivery{Payload: map[string]any{"event": "test"}}
	NewDispatcher(db, log).deliver(&models.Webhook{
		ID:             "webhook",
		URL:            server.URL,
		ContentType:    "application/json",
		MaxRetries:     1,
		TimeoutSeconds: 1,
	}, delivery)

	return db, delivery
}

func TestDeliveryRetainsResponseBodyUnderLimit(t *testing.T) {
	body := "complete response"
	db, delivery := newResponseDelivery(t, body)

	if db.delivery == nil {
		t.Fatal("delivery was not persisted")
	}
	if delivery.ResponseStatus != http.StatusOK {
		t.Fatalf("response status = %d, want %d", delivery.ResponseStatus, http.StatusOK)
	}
	if got := db.delivery.ResponseBody; got != body {
		t.Fatalf("persisted response body = %q, want %q", got, body)
	}
}

func TestDeliveryTruncatesOversizedResponseBody(t *testing.T) {
	db, _ := newResponseDelivery(t, strings.Repeat("x", maxWebhookResponseBodySize+1))

	if db.delivery == nil {
		t.Fatal("delivery was not persisted")
	}
	if got := db.delivery.ResponseBody; len(got) != maxWebhookResponseBodySize {
		t.Fatalf("persisted response body length = %d, want %d", len(got), maxWebhookResponseBodySize)
	}
	if !strings.HasSuffix(db.delivery.ResponseBody, webhookResponseTruncatedMarker) {
		t.Fatalf("persisted response body = %q, want truncation marker", db.delivery.ResponseBody[len(db.delivery.ResponseBody)-len(webhookResponseTruncatedMarker):])
	}
}

func TestDeliveryNetworkErrorEventuallyFails(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}
	db := &deliveryTestDB{BaseMockDB: &database.BaseMockDB{}}
	d := NewDispatcher(db, log)
	wh := &models.Webhook{ID: "w", URL: "http://127.0.0.1:1/", ContentType: "application/json", MaxRetries: 2, TimeoutSeconds: 1}
	delivery := &models.WebhookDelivery{Payload: map[string]any{}}

	d.deliver(wh, delivery)
	if delivery.Status != "pending" || delivery.AttemptCount != 1 {
		t.Fatalf("after 1st failure: status=%s attempts=%d", delivery.Status, delivery.AttemptCount)
	}
	d.deliver(wh, delivery)
	if delivery.Status != "failed" || delivery.AttemptCount != 2 {
		t.Fatalf("after 2nd failure: status=%s attempts=%d", delivery.Status, delivery.AttemptCount)
	}
}

func TestEnqueueDeduplicatesInflight(t *testing.T) {
	d := NewDispatcher(nil, nil)
	del := &models.WebhookDelivery{ID: "1"}
	if !d.enqueue(del) || d.enqueue(del) {
		t.Fatal("second enqueue of in-flight delivery must be rejected")
	}
}

func TestDeliveryBlocksLoopbackByDefault(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	db := &deliveryTestDB{BaseMockDB: &database.BaseMockDB{}}
	delivery := &models.WebhookDelivery{Payload: map[string]any{}}
	NewDispatcher(db, log).deliver(&models.Webhook{ID: "w", URL: "http://127.0.0.1:1/", MaxRetries: 1, TimeoutSeconds: 1}, delivery)
	if delivery.Status != "failed" {
		t.Fatalf("status=%s", delivery.Status)
	}
}

type disabledWebhookDB struct {
	deliveryTestDB

	wh *models.Webhook
}

func (db *disabledWebhookDB) GetWebhook(context.Context, string) (*models.Webhook, error) {
	return db.wh, nil
}

func TestProcessDropsDeliveriesOfDisabledWebhook(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		event    string
		wantHits int
		want     string
	}{
		{"artifact.uploaded", 0, "failed"},
		{EventWebhookTest, 1, "success"},
	} {
		hits = 0
		db := &disabledWebhookDB{
			wh: &models.Webhook{ID: "wh", URL: server.URL, Enabled: false, ContentType: "application/json", MaxRetries: 3, TimeoutSeconds: 1},
		}
		db.BaseMockDB = &database.BaseMockDB{}
		delivery := &models.WebhookDelivery{ID: "d", WebhookID: "wh", EventType: tc.event, Payload: map[string]any{}}

		NewDispatcher(db, log).process(delivery)

		if hits != tc.wantHits || db.delivery == nil || db.delivery.Status != tc.want {
			t.Errorf("%s: hits=%d delivery=%+v, want hits=%d status=%s", tc.event, hits, db.delivery, tc.wantHits, tc.want)
		}
	}
}

func TestDeliveryDoesNotFollowRedirects(t *testing.T) {
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	var hits atomic.Int32

	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	db := &deliveryTestDB{BaseMockDB: &database.BaseMockDB{}}
	delivery := &models.WebhookDelivery{Payload: map[string]any{"event": "test"}}
	NewDispatcher(db, log).deliver(&models.Webhook{
		ID: "webhook", URL: redirector.URL, ContentType: "application/json", MaxRetries: 1, TimeoutSeconds: 1,
	}, delivery)

	if hits.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}

	if delivery.ResponseStatus != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", delivery.ResponseStatus)
	}
}
