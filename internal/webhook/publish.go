// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"context"
	"time"
)

// Publisher is the slice of *Dispatcher that request handlers use to announce
// events. Handlers hold it as an optional dependency: a nil Publisher (tests,
// webhooks not wired) turns every Emit into a no-op.
type Publisher interface {
	Dispatch(ctx context.Context, repoID, eventType string, payload map[string]any)
}

// emitTimeout bounds one Emit: Dispatch performs a webhook lookup plus one
// insert per matching webhook.
const emitTimeout = 10 * time.Second

// Emit publishes an event through p, if any. The event is detached from the
// request's cancellation (a client hanging up right after a successful upload
// must not lose the event) but bounded by emitTimeout. Failures are logged by
// the dispatcher and never surface to the caller: an event that cannot be
// recorded must not fail the operation that produced it.
func Emit(ctx context.Context, p Publisher, repoID, eventType string, payload map[string]any) {
	if p == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitTimeout)
	defer cancel()

	p.Dispatch(ctx, repoID, eventType, payload)
}

// BundlePromotion describes a completed release bundle promotion.
type BundlePromotion struct {
	Repository string // target repository name
	Format     string // target repository format
	Bundle     string // bundle name
	Version    string // bundle version
	FromStage  string // stage the bundle left ("" when it had none)
	ToStage    string // stage the bundle reached
	Items      int    // bundle items promoted
	Copied     int    // artifacts newly written to the target
	Existing   int    // artifacts the target already held with the same content
	Replaced   int    // artifacts a forced promotion moved to the target's trash
}

// BundlePromotedPayload builds the JSON payload of a bundle.promoted event.
func BundlePromotedPayload(b BundlePromotion) map[string]any {
	return map[string]any{
		"event":      EventBundlePromoted,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"repository": b.Repository,
		"format":     b.Format,
		"type":       "hosted",
		"bundle":     map[string]any{"name": b.Bundle, "version": b.Version},
		"from_stage": b.FromStage,
		"to_stage":   b.ToStage,
		"items":      b.Items,
		"copied":     b.Copied,
		"existing":   b.Existing,
		"replaced":   b.Replaced,
	}
}

// Artifact describes the artifact an artifact.* event is about.
type Artifact struct {
	Repository string // repository name
	Format     string // repository format (npm, deb, ...)
	Name       string // package / artifact name
	Version    string // version, when the format has one
	Path       string // path or file name within the repository
	SHA256     string // content digest, when known
	Size       int64  // size in bytes, when known
}

// ArtifactPayload builds the JSON payload of an artifact.* event.
func ArtifactPayload(eventType string, a Artifact) map[string]any {
	artifact := map[string]any{
		"name": a.Name,
		"path": a.Path,
	}

	if a.Version != "" {
		artifact["version"] = a.Version
	}

	if a.SHA256 != "" {
		artifact["sha256"] = a.SHA256
	}

	if a.Size > 0 {
		artifact["size"] = a.Size
	}

	return map[string]any{
		"event":      eventType,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"repository": a.Repository,
		"format":     a.Format,
		"artifact":   artifact,
	}
}

// RepositoryPayload builds the JSON payload of a repository.* event.
func RepositoryPayload(eventType, name, format, repoType string) map[string]any {
	return map[string]any{
		"event":      eventType,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"repository": name,
		"format":     format,
		"type":       repoType,
	}
}
