// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/webhook"
)

// UploadAuditPublisher is a webhook.Publisher that forwards every event to the
// wrapped publisher and records each artifact upload as an artifact.uploaded
// audit event against its repository, naming the package and version and the
// authenticated caller. Every format handler announces a committed upload
// through the publisher, so this is the one place that sees them all; the
// generic request row of AuditMiddleware knows neither the repository nor the
// package, and also exists for uploads that failed.
type UploadAuditPublisher struct {
	next webhook.Publisher
}

// NewUploadAuditPublisher wraps next (which may be nil).
func NewUploadAuditPublisher(next webhook.Publisher) *UploadAuditPublisher {
	return &UploadAuditPublisher{next: next}
}

// Dispatch implements webhook.Publisher.
func (u *UploadAuditPublisher) Dispatch(ctx context.Context, repoID, eventType string, payload map[string]any) {
	if eventType == webhook.EventArtifactUploaded && repoID != "" {
		auditUpload(ctx, repoID, payload)
	}

	if u.next != nil {
		u.next.Dispatch(ctx, repoID, eventType, payload)
	}
}

func auditUpload(ctx context.Context, repoID string, payload map[string]any) {
	l := audit.Default()
	if l == nil {
		return
	}

	artifact, _ := payload["artifact"].(map[string]any)
	name, _ := artifact["name"].(string)
	version, _ := artifact["version"].(string)

	if name == "" {
		name, _ = artifact["path"].(string)
	}

	if name == "" {
		return
	}

	subject := name
	if version != "" {
		subject += " " + version
	}

	resourceType := "artifact"
	ev := audit.AuditEvent{
		Action:       audit.ActionArtifactUploaded,
		ActorType:    "system",
		RepositoryID: &repoID,
		ResourceType: &resourceType,
		Details:      "uploaded: " + subject,
		Package:      name,
		Version:      version,
		Subject:      subject,
	}

	if p := PrincipalFromContext(ctx); p != nil && !p.IsAnonymous {
		ev.ActorType = "user"
		ev.Actor = p.Username
		ev.ActorID = p.UserID

		if p.IsToken() {
			ev.ActorType = "token"
		}
	}

	l.Log(ctx, ev)
}
