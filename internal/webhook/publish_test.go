// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type captured struct {
	ctxErr      error // ctx.Err() as seen inside Dispatch
	hasDeadline bool
	repoID      string
	event       string
	payload     map[string]any
}

type capturePublisher struct {
	mu  sync.Mutex
	got []captured
}

func (p *capturePublisher) Dispatch(ctx context.Context, repoID, eventType string, payload map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	_, hasDeadline := ctx.Deadline()
	p.got = append(p.got, captured{ctx.Err(), hasDeadline, repoID, eventType, payload})
}

func TestEmitNilPublisherIsNoop(t *testing.T) {
	Emit(context.Background(), nil, "r", EventArtifactUploaded, nil) // must not panic
}

func TestEmitDetachesFromRequestCancellation(t *testing.T) {
	p := &capturePublisher{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the client hung up right after the upload committed

	Emit(ctx, p, "repo-1", EventArtifactUploaded, map[string]any{"k": "v"})

	if len(p.got) != 1 {
		t.Fatalf("events = %d, want 1", len(p.got))
	}

	c := p.got[0]
	if c.repoID != "repo-1" || c.event != EventArtifactUploaded || c.payload["k"] != "v" {
		t.Errorf("unexpected dispatch: %+v", c)
	}

	if c.ctxErr != nil {
		t.Error("dispatch context inherited the request's cancellation: the event would be lost")
	}

	if !c.hasDeadline {
		t.Error("dispatch context must be bounded by a timeout")
	}
}

func TestArtifactPayload(t *testing.T) {
	p := ArtifactPayload(EventArtifactDeleted, Artifact{Repository: "r", Format: "npm", Name: "left-pad", Version: "1.0.0", Path: "left-pad/-/left-pad-1.0.0.tgz", SHA256: "abc", Size: 12})

	if p["event"] != EventArtifactDeleted || p["repository"] != "r" || p["format"] != "npm" {
		t.Errorf("envelope = %v", p)
	}

	art, ok := p["artifact"].(map[string]any)
	if !ok || art["name"] != "left-pad" || art["version"] != "1.0.0" || art["sha256"] != "abc" || art["size"] != int64(12) {
		t.Errorf("artifact = %v", p["artifact"])
	}

	bare := ArtifactPayload(EventArtifactUploaded, Artifact{Name: "n", Path: "p"})["artifact"].(map[string]any)
	for _, k := range []string{"version", "sha256", "size"} {
		if _, present := bare[k]; present {
			t.Errorf("unset %s must be omitted", k)
		}
	}
}

// claimDB hands out one pending delivery and records new ones.
type claimDB struct {
	*databasetest.BaseMockDB

	mu       sync.Mutex
	hooks    []*models.Webhook
	created  []*models.WebhookDelivery
	claimed  []*models.WebhookDelivery
	gotLimit int
	gotLease time.Duration
}

func (db *claimDB) ListWebhooksForEvent(context.Context, string, string) ([]*models.Webhook, error) {
	return db.hooks, nil
}

func (db *claimDB) CreateWebhookDelivery(_ context.Context, d *models.WebhookDelivery) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	d.ID = "d-new"
	db.created = append(db.created, d)

	return nil
}

func (db *claimDB) ClaimPendingWebhookDeliveries(_ context.Context, limit int, lease time.Duration) ([]*models.WebhookDelivery, error) {
	db.gotLimit, db.gotLease = limit, lease

	return db.claimed, nil
}

func newTestLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	return log
}

func TestDispatchInsertsLeasedDeliveries(t *testing.T) {
	db := &claimDB{BaseMockDB: &databasetest.BaseMockDB{}, hooks: []*models.Webhook{{ID: "w1"}, {ID: "w2"}}}
	d := NewDispatcher(db, newTestLogger(t))

	before := time.Now()

	d.Dispatch(context.Background(), "repo", EventRepoCreated, map[string]any{"a": 1})

	if len(db.created) != 2 {
		t.Fatalf("created %d deliveries, want 2", len(db.created))
	}

	for _, del := range db.created {
		if del.Status != "pending" || del.EventType != EventRepoCreated {
			t.Errorf("delivery = %+v", del)
		}

		if del.NextRetryAt == nil || !del.NextRetryAt.After(before.Add(claimLease/2)) {
			t.Errorf("fresh delivery must be inserted pre-leased so other instances skip it, NextRetryAt = %v", del.NextRetryAt)
		}
	}
}

func TestPollPendingClaimsWithLeaseAndQueuesEachOnce(t *testing.T) {
	db := &claimDB{BaseMockDB: &databasetest.BaseMockDB{}, claimed: []*models.WebhookDelivery{{ID: "a"}, {ID: "b"}}}
	d := NewDispatcher(db, newTestLogger(t))

	d.pollPending()
	d.pollPending() // a second tick must not queue what is still in flight

	if db.gotLimit != claimBatch || db.gotLease != claimLease {
		t.Errorf("claimed with limit %d lease %v, want %d / %v", db.gotLimit, db.gotLease, claimBatch, claimLease)
	}

	if got := len(d.queue); got != 2 {
		t.Errorf("queue length = %d, want 2 (each claimed delivery queued once)", got)
	}
}

func TestBundlePromotedIsSubscribableAndSummarized(t *testing.T) {
	if !IsSubscribableEvent(EventBundlePromoted) {
		t.Fatal("bundle.promoted must be subscribable")
	}

	payload := BundlePromotedPayload(BundlePromotion{
		Repository: "releases", Format: "maven", Bundle: "shop", Version: "2.4.0",
		FromStage: "qa", ToStage: "prod", Items: 3, Copied: 2, Existing: 1,
	})

	if payload["event"] != EventBundlePromoted || payload["repository"] != "releases" || payload["to_stage"] != "prod" ||
		payload["copied"] != 2 || payload["existing"] != 1 || payload["items"] != 3 {
		t.Errorf("payload = %v", payload)
	}

	s := summarize(&models.WebhookDelivery{EventType: EventBundlePromoted, Payload: payload})

	if s.title != "Bundle promoted" || s.severity != severityGood {
		t.Errorf("title %q severity %v", s.title, s.severity)
	}

	if s.headline != "Bundle promoted: shop 2.4.0 to releases" {
		t.Errorf("headline = %q", s.headline)
	}

	facts := map[string]string{}
	for _, f := range s.facts {
		facts[f.name] = f.value
	}

	for name, want := range map[string]string{"Bundle": "shop 2.4.0", "Stage": "prod", "Copied": "2", "Already present": "1", "Repository": "releases"} {
		if facts[name] != want {
			t.Errorf("fact %s = %q, want %q (all: %v)", name, facts[name], want, facts)
		}
	}
}
