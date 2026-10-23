// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package dtrack

import (
	"context"

	"github.com/openartifact/openartifact/internal/webhook"
)

// UploadPublisher is a webhook.Publisher that forwards every event to the
// wrapped publisher and, for artifact uploads, schedules a Dependency-Track
// push when the repository has the integration and auto-push on.
type UploadPublisher struct {
	next   webhook.Publisher
	pusher *Pusher
}

// NewUploadPublisher wraps next (which may be nil).
func NewUploadPublisher(next webhook.Publisher, p *Pusher) *UploadPublisher {
	return &UploadPublisher{next: next, pusher: p}
}

// Dispatch implements webhook.Publisher.
func (u *UploadPublisher) Dispatch(ctx context.Context, repoID, eventType string, payload map[string]any) {
	if eventType == webhook.EventArtifactUploaded {
		if id := ArtifactIDFromPayload(payload); id != "" {
			u.pusher.EnqueueIfEnabled(ctx, Job{RepoID: repoID, ArtifactID: id, Trigger: TriggerUpload})
		}
	}

	if u.next != nil {
		u.next.Dispatch(ctx, repoID, eventType, payload)
	}
}

// ArtifactIDFromPayload is the "name@version" id of an artifact event, "" when
// the event names no artifact.
func ArtifactIDFromPayload(payload map[string]any) string {
	a, _ := payload["artifact"].(map[string]any)
	name, _ := a["name"].(string)
	version, _ := a["version"].(string)

	switch {
	case name == "":
		return ""
	case version == "":
		return name
	default:
		return name + "@" + version
	}
}
