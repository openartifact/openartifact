// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// hookConcurrency bounds how many hook runs execute at once.
	hookConcurrency = 16
	// hookBacklog bounds the hook runs waiting for a slot; beyond it an event
	// is dropped (and logged) rather than piling up behind a slow hook.
	hookBacklog = 1024
	// hookTimeout bounds one hook run.
	hookTimeout = 2 * time.Minute
)

// ManifestEvent describes a manifest that was just pushed (PUT) to a hosted
// docker repository and committed.
type ManifestEvent struct {
	// RepoID and RepoName identify the OpenArtifact repository.
	RepoID   string
	RepoName string
	// Image is the image path inside the repository ("" is the default image).
	Image string
	// Tag is the tag the manifest was pushed under; empty for a push by digest.
	Tag string
	// Digest is the manifest's digest ("sha256:<hex>") and MediaType the
	// Content-Type it was pushed with.
	Digest    string
	MediaType string
	// ArtifactType is the manifest's artifactType, or the media type of its
	// config when it declares none (the Referrers API convention); empty for an
	// image index.
	ArtifactType string
	// SubjectDigest is the digest the manifest refers to (OCI subject); empty
	// when it has none.
	SubjectDigest string
	// Annotations are the manifest's own annotations.
	Annotations map[string]string
	// Manifest is the raw manifest body. Hooks must not modify it.
	Manifest []byte
	// ConfigLabels returns the labels of the image config blob the manifest
	// references (empty for an index, an artifact manifest or an unreadable
	// config). The config is read once per event.
	ConfigLabels func(ctx context.Context) map[string]string
}

// ManifestPutHook is called after a manifest push committed. It runs outside
// the request, on its own context, so it neither delays nor fails the push; its
// work should be idempotent (a manifest can be pushed again) and its errors are
// its own to log.
type ManifestPutHook func(ctx context.Context, ev ManifestEvent)

// hookRunner runs the registered ManifestPutHooks off the request path with
// bounded concurrency.
type hookRunner struct {
	log     *logger.Logger
	hooks   []ManifestPutHook
	slots   chan struct{}
	pending atomic.Int64
	wg      sync.WaitGroup
}

func newHookRunner(log *logger.Logger) *hookRunner {
	return &hookRunner{log: log, slots: make(chan struct{}, hookConcurrency)}
}

// fire runs every hook for ev in the background. It returns at once.
func (r *hookRunner) fire(ctx context.Context, ev ManifestEvent) {
	if len(r.hooks) == 0 {
		return
	}

	if r.pending.Add(1) > hookBacklog {
		r.pending.Add(-1)
		r.log.Warnw("docker manifest hooks saturated; event dropped", "repository", ev.RepoName, "digest", ev.Digest)

		return
	}

	// The request context ends with the response; the hooks outlive it.
	ctx = context.WithoutCancel(ctx)

	r.wg.Go(func() {
		defer r.pending.Add(-1)

		r.slots <- struct{}{}

		defer func() { <-r.slots }()

		r.run(ctx, ev)
	})
}

func (r *hookRunner) run(ctx context.Context, ev ManifestEvent) {
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	for _, hook := range r.hooks {
		r.runOne(ctx, hook, ev)
	}
}

// runOne contains a panic of one hook to that hook.
func (r *hookRunner) runOne(ctx context.Context, hook ManifestPutHook, ev ManifestEvent) {
	defer func() {
		if p := recover(); p != nil {
			r.log.Errorw("docker manifest hook panicked", "repository", ev.RepoName, "digest", ev.Digest, "panic", p)
		}
	}()

	hook(ctx, ev)
}

// AddManifestPutHook registers a hook called after every committed manifest
// push. It must be called before the handler serves requests.
func (h *HTTPHandler) AddManifestPutHook(hook ManifestPutHook) {
	h.hooks.hooks = append(h.hooks.hooks, hook)
}

// newManifestEvent builds the event of a committed push. The config labels are read
// at most once, on first use.
func newManifestEvent(
	repoID, repoName, image, tag, digest, mediaType string,
	body []byte,
	meta ociManifestMeta,
	labels func(context.Context) map[string]string,
) ManifestEvent {
	ev := ManifestEvent{
		RepoID:       repoID,
		RepoName:     repoName,
		Image:        image,
		Tag:          tag,
		Digest:       digest,
		MediaType:    mediaType,
		ArtifactType: meta.referrerArtifactType(),
		Annotations:  meta.Annotations,
		Manifest:     body,
		ConfigLabels: labels,
	}

	if meta.Subject != nil {
		ev.SubjectDigest = meta.Subject.Digest
	}

	return ev
}
