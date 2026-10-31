// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/webhook"
)

type nextPublisher struct{ events []string }

func (n *nextPublisher) Dispatch(_ context.Context, _, eventType string, _ map[string]any) {
	n.events = append(n.events, eventType)
}

func uploadAuditHarness(t *testing.T) (*nextPublisher, *UploadAuditPublisher, func() []*models.AuditLog) {
	t.Helper()

	db := &auditCaptureDB{events: make(chan *models.AuditLog, 8)}
	l := audit.NewLogger(db, nil, nil, 8)
	audit.SetDefault(l)

	t.Cleanup(func() { audit.SetDefault(nil) })

	next := &nextPublisher{}

	return next, NewUploadAuditPublisher(next), func() []*models.AuditLog {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = l.Shutdown(ctx)
		close(db.events)

		var out []*models.AuditLog
		for e := range db.events {
			out = append(out, e)
		}

		return out
	}
}

func TestUploadAuditPublisherRecordsUploadsAgainstTheirRepository(t *testing.T) {
	next, pub, rows := uploadAuditHarness(t)

	uid := "u-1"
	ctx := WithPrincipal(context.Background(), &auth.Principal{UserID: &uid, Username: "alice", Credential: auth.CredentialSession})
	payload := webhook.ArtifactPayload(
		webhook.EventArtifactUploaded,
		webhook.Artifact{Repository: "libs", Name: "lib", Version: "1.0", Path: "lib-1.0.tgz"},
	)

	pub.Dispatch(ctx, "repo-1", webhook.EventArtifactUploaded, payload)

	got := rows()
	if len(got) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(got))
	}

	row := got[0]
	if row.Action != audit.ActionArtifactUploaded || row.RepositoryID == nil || *row.RepositoryID != "repo-1" ||
		row.ResourceType == nil || *row.ResourceType != "artifact" {
		t.Errorf("row = %+v", row)
	}

	var d struct{ Message, Actor, Package, Version, Subject string }
	if err := json.Unmarshal(row.DetailsJSON, &d); err != nil {
		t.Fatal(err)
	}

	if d.Actor != "alice" || d.Package != "lib" || d.Version != "1.0" || d.Subject != "lib 1.0" {
		t.Errorf("details = %+v", d)
	}

	if len(next.events) != 1 || next.events[0] != webhook.EventArtifactUploaded {
		t.Errorf("the wrapped publisher must still get the event: %v", next.events)
	}
}

func TestUploadAuditPublisherIgnoresOtherEventsAndNamelessUploads(t *testing.T) {
	next, pub, rows := uploadAuditHarness(t)
	ctx := context.Background()

	pub.Dispatch(
		ctx,
		"repo-1",
		webhook.EventArtifactDeleted,
		webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhook.Artifact{Name: "lib"}),
	)
	pub.Dispatch(ctx, "repo-1", webhook.EventArtifactUploaded, map[string]any{})
	pub.Dispatch(
		ctx,
		"",
		webhook.EventArtifactUploaded,
		webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{Name: "lib"}),
	)

	if got := rows(); len(got) != 0 {
		t.Errorf("audit rows = %d, want none", len(got))
	}

	if len(next.events) != 3 {
		t.Errorf("every event must reach the wrapped publisher: %v", next.events)
	}
}

func TestUploadAuditPublisherWithoutWrappedPublisher(t *testing.T) {
	_, _, rows := uploadAuditHarness(t)

	pub := NewUploadAuditPublisher(nil)
	pub.Dispatch(context.Background(), "repo-1", webhook.EventArtifactUploaded,
		webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhook.Artifact{Name: "f.txt"}))

	got := rows()
	if len(got) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(got))
	}
}
