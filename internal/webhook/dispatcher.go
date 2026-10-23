// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Event types.
const (
	EventArtifactUploaded = "artifact.uploaded"
	EventArtifactDeleted  = "artifact.deleted"
	EventRepoCreated      = "repository.created"
	EventRepoDeleted      = "repository.deleted"
	// EventArtifactRestored and EventRepoRestored fire when an item leaves the
	// trash; EventArtifactPurged and EventRepoPurged when it is removed for
	// good (by an admin or the retention purge). The deleted events fire for
	// every delete, soft or hard.
	EventArtifactRestored = "artifact.restored"
	EventArtifactPurged   = "artifact.purged"
	EventRepoRestored     = "repository.restored"
	EventRepoPurged       = "repository.purged"
	// EventBundlePromoted fires once a release bundle has been copied into the
	// target repository of a pipeline stage. It is delivered to the webhooks of
	// the target repository and the global ones.
	EventBundlePromoted = "bundle.promoted"
)

const (
	maxWebhookResponseBodySize     = 64 * 1024
	webhookResponseTruncatedMarker = "\n[response body truncated]"
)

type Dispatcher struct {
	db     database.Database
	log    *logger.Logger
	client *http.Client
	// insecureClient skips TLS verification for webhooks with ssl_verify=false.
	insecureClient *http.Client
	// source is the CloudEvents source of cloudevents-format deliveries.
	source   string
	queue    chan *models.WebhookDelivery
	stopCh   chan struct{}
	doneCh   chan struct{}
	inflight sync.Map // delivery ID -> struct{}; prevents duplicate concurrent deliveries
	wg       sync.WaitGroup
	stopOnce sync.Once
	started  atomic.Bool
	ctx      context.Context //nolint:containedctx // lifecycle context for in-flight deliveries; cancelled by Stop
	cancel   context.CancelFunc
}

const (
	deliveryWorkers = 8

	// claimLease is how long a claimed delivery stays invisible to other
	// pollers. It must exceed the longest delivery attempt (MaxTimeoutSeconds)
	// so a slow endpoint is not redelivered by another instance; a crashed
	// instance's claims become due again once it expires.
	claimLease = 2 * time.Minute
	// claimBatch bounds the deliveries claimed per retry-loop tick.
	claimBatch = 100
)

func NewDispatcher(db database.Database, log *logger.Logger) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		db:             db,
		log:            log,
		client:         newWebhookClient(30 * time.Second),
		insecureClient: newInsecureClient(30 * time.Second),
		queue:          make(chan *models.WebhookDelivery, 1000),
		stopCh:         make(chan struct{}),
		doneCh:         make(chan struct{}),
		ctx:            ctx,
		cancel:         cancel,
	}
}

// SetBaseURL sets the server's external base URL, used as the CloudEvents
// source of cloudevents-format deliveries. Call it before Start.
func (d *Dispatcher) SetBaseURL(baseURL string) {
	d.source = strings.TrimRight(baseURL, "/")
}

// enqueue claims the delivery (so the retry loop and Dispatch never process the
// same row twice) and queues it. Returns false if already claimed or queue full.
func (d *Dispatcher) enqueue(delivery *models.WebhookDelivery) bool {
	if _, loaded := d.inflight.LoadOrStore(delivery.ID, struct{}{}); loaded {
		return false
	}
	select {
	case d.queue <- delivery:
		return true
	default:
		d.inflight.Delete(delivery.ID)
		return false
	}
}

// newLeasedDelivery builds a pending delivery that is inserted already leased
// (next_retry_at = now + claimLease), so pollers on other instances skip it
// while this instance delivers it from its in-memory queue.
func newLeasedDelivery(webhookID, eventType string, payload map[string]any) *models.WebhookDelivery {
	lease := time.Now().Add(claimLease)

	return &models.WebhookDelivery{
		WebhookID:   webhookID,
		EventType:   eventType,
		Payload:     payload,
		Status:      "pending",
		NextRetryAt: &lease,
	}
}

// Dispatch queues a webhook event for all matching webhooks.
// repoID can be empty for global events.
func (d *Dispatcher) Dispatch(ctx context.Context, repoID, eventType string, payload map[string]any) {
	webhooks, err := d.db.ListWebhooksForEvent(ctx, repoID, eventType)
	if err != nil {
		d.log.Errorw("failed to list webhooks for event", "event", eventType, "error", err)
		return
	}

	for _, wh := range webhooks {
		delivery := newLeasedDelivery(wh.ID, eventType, payload)
		if err := d.db.CreateWebhookDelivery(ctx, delivery); err != nil {
			d.log.Errorw("failed to create webhook delivery", "webhook_id", wh.ID, "error", err)
			continue
		}

		if !d.enqueue(delivery) {
			d.log.Warnw("webhook delivery queue full; delivery left pending for retry loop", "webhook_id", wh.ID)
		}
	}
}

// Start begins processing the delivery queue and retry loop.
func (d *Dispatcher) Start() {
	d.started.Store(true)
	for range deliveryWorkers {
		d.wg.Add(1)
		go d.worker()
	}

	d.wg.Add(1)
	go d.retryLoop()

	go func() {
		d.wg.Wait()
		close(d.doneCh)
	}()
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for {
		select {
		case delivery := <-d.queue:
			d.process(delivery)
		case <-d.stopCh:
			return
		}
	}
}

func (d *Dispatcher) process(delivery *models.WebhookDelivery) {
	defer d.inflight.Delete(delivery.ID)
	defer func() {
		if r := recover(); r != nil {
			d.log.Errorw("panic delivering webhook", "panic", r)
		}
	}()

	wh, err := d.db.GetWebhook(d.ctx, delivery.WebhookID)
	if err != nil {
		d.log.Errorw("failed to get webhook", "webhook_id", delivery.WebhookID, "error", err)
		return
	}
	if wh == nil {
		d.log.Warnw("webhook not found", "webhook_id", delivery.WebhookID)
		return
	}
	// A disabled webhook must not keep receiving queued deliveries or retries
	// (the admin may have disabled it because the endpoint is compromised).
	// webhook.test is sent directly to one webhook and is exempt.
	if !wh.Enabled && delivery.EventType != EventWebhookTest {
		delivery.Status = "failed"
		delivery.NextRetryAt = nil

		if err := d.db.UpdateWebhookDelivery(context.Background(), delivery); err != nil {
			d.log.Errorw("failed to update webhook delivery", "webhook_id", wh.ID, "error", err)
		}

		d.log.Infow("webhook disabled; delivery dropped", "webhook_id", wh.ID, "delivery_id", delivery.ID)

		return
	}

	d.deliver(wh, delivery)
}

func (d *Dispatcher) retryLoop() {
	defer d.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.pollPending()
		case <-d.stopCh:
			return
		}
	}
}

// pollPending claims the due deliveries (new retries, plus any whose lease
// expired because the instance that held them died) and queues them.
func (d *Dispatcher) pollPending() {
	defer func() {
		if r := recover(); r != nil {
			d.log.Errorw("panic in webhook retry loop", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()

	deliveries, err := d.db.ClaimPendingWebhookDeliveries(ctx, claimBatch, claimLease)
	if err != nil {
		d.log.Errorw("failed to claim pending webhook deliveries", "error", err)
		return
	}

	for _, delivery := range deliveries {
		d.enqueue(delivery)
	}
}

// Stop gracefully shuts down the dispatcher. Safe to call more than once and
// without a prior Start. In-flight deliveries are cancelled after a grace period.
func (d *Dispatcher) Stop() {
	d.stopOnce.Do(func() { close(d.stopCh) })
	if !d.started.Load() {
		d.cancel()
		return
	}

	select {
	case <-d.doneCh:
	case <-time.After(10 * time.Second):
		d.cancel()
		select {
		case <-d.doneCh:
		case <-time.After(5 * time.Second):
		}
	}
	d.cancel()
}

// deliver attempts to deliver a webhook.
func (d *Dispatcher) deliver(wh *models.Webhook, delivery *models.WebhookDelivery) {
	payloadJSON, contentType, err := renderBody(wh, delivery, d.source)
	if err != nil {
		d.log.Errorw("failed to marshal payload", "webhook_id", wh.ID, "error", err)
		delivery.AttemptCount = wh.MaxRetries // permanent: payload can never marshal
		d.markFailed(delivery, wh)

		return
	}

	timeout := time.Duration(wh.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(payloadJSON))
	if err != nil {
		d.log.Errorw("failed to create request", "webhook_id", wh.ID, "error", err)
		delivery.AttemptCount++
		d.markFailed(delivery, wh)

		return
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "OpenArtifact-Webhook/1.0")

	// Add HMAC signature if secret is provided
	if wh.SecretEnc != "" {
		secret, derr := crypto.Decrypt(wh.SecretEnc)
		if derr != nil {
			d.log.Errorw("failed to decrypt webhook secret", "webhook_id", wh.ID, "error", derr)
			delivery.AttemptCount++
			d.markFailed(delivery, wh)

			return
		}
		signature := computeHMAC(payloadJSON, []byte(secret))
		req.Header.Set("X-Hub-Signature-256", "sha256="+signature)
	}

	// Make the request
	resp, err := d.clientFor(wh).Do(req)
	if err != nil {
		d.log.Errorw("failed to send webhook", "webhook_id", wh.ID, "error", err)
		delivery.AttemptCount++
		d.markFailed(delivery, wh)

		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response body.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxWebhookResponseBodySize+1))
	if err != nil {
		d.log.Errorw("failed to read response body", "webhook_id", wh.ID, "error", err)
	}
	if len(respBody) > maxWebhookResponseBodySize {
		respBody = append(respBody[:maxWebhookResponseBodySize-len(webhookResponseTruncatedMarker)], webhookResponseTruncatedMarker...)
	}

	delivery.ResponseStatus = resp.StatusCode
	delivery.ResponseBody = string(respBody)
	delivery.AttemptCount++

	// Check if successful
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		delivery.Status = "success"
		now := time.Now()

		delivery.DeliveredAt = &now
		delivery.NextRetryAt = nil
		if err := d.db.UpdateWebhookDelivery(context.Background(), delivery); err != nil {
			d.log.Errorw("failed to update webhook delivery", "webhook_id", wh.ID, "error", err)
		}

		d.log.Infow("webhook delivered successfully", "webhook_id", wh.ID, "status", resp.StatusCode)

		return
	}

	// Failed - check if we should retry
	d.markFailed(delivery, wh)
}

// markFailed marks a delivery as failed and schedules retry if applicable.
// Callers MUST have incremented delivery.AttemptCount for the attempt.
func (d *Dispatcher) markFailed(delivery *models.WebhookDelivery, wh *models.Webhook) {
	if delivery.AttemptCount < wh.MaxRetries {
		// Exponential backoff: 2^(attempt-1) * 30s, capped at 1h.
		exp := max(delivery.AttemptCount-1, 0)
		backoffSeconds := math.Min(math.Pow(2, float64(exp))*30, 3600)
		nextRetry := time.Now().Add(time.Duration(backoffSeconds) * time.Second)
		delivery.NextRetryAt = &nextRetry
		delivery.Status = "pending"
	} else {
		// Max retries exceeded
		delivery.NextRetryAt = nil
		delivery.Status = "failed"
	}

	if err := d.db.UpdateWebhookDelivery(context.Background(), delivery); err != nil {
		d.log.Errorw("failed to update webhook delivery", "webhook_id", wh.ID, "error", err)
	}

	if delivery.Status == "failed" {
		d.log.Warnw("webhook delivery failed permanently", "webhook_id", wh.ID, "attempts", delivery.AttemptCount)
	} else {
		d.log.Infow("webhook delivery scheduled for retry", "webhook_id", wh.ID, "attempts", delivery.AttemptCount)
	}
}

// computeHMAC computes HMAC-SHA256 signature.
func computeHMAC(data, secret []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write(data)

	return hex.EncodeToString(h.Sum(nil))
}
