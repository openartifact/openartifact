// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
)

const (
	// DefaultTimeoutSeconds is the per-delivery timeout of a new webhook.
	DefaultTimeoutSeconds = 30
	// MaxTimeoutSeconds is the largest accepted per-delivery timeout (the
	// dispatcher's HTTP clients enforce the same 30s ceiling).
	MaxTimeoutSeconds = 30
)

// EventWebhookTest is only ever sent directly to one webhook (see DispatchTo);
// it cannot be subscribed to.
const EventWebhookTest = "webhook.test"

// SubscribableEvents returns the event names a webhook may subscribe to.
func SubscribableEvents() []string {
	return []string{
		EventArtifactUploaded,
		EventArtifactDeleted,
		EventRepoCreated,
		EventRepoDeleted,
		EventArtifactRestored,
		EventArtifactPurged,
		EventRepoRestored,
		EventRepoPurged,
	}
}

// IsSubscribableEvent reports whether name is an event webhooks can subscribe to.
func IsSubscribableEvent(name string) bool { return slices.Contains(SubscribableEvents(), name) }

// newWebhookClient returns the SSRF-safe client for webhook delivery. Redirects
// are never followed: 307/308 would replay the signed body and
// X-Hub-Signature-256 to a host the owner never configured. A 3xx response is
// recorded as a failed delivery.
func newWebhookClient(timeout time.Duration) *http.Client {
	c := netutil.SafeHTTPClient(timeout)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return c
}

// newInsecureClient returns the SSRF-safe client with TLS certificate
// verification disabled, used for webhooks created with ssl_verify=false.
func newInsecureClient(timeout time.Duration) *http.Client {
	c := newWebhookClient(timeout)
	if tr, ok := c.Transport.(*http.Transport); ok {
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} //nolint:gosec // explicit per-webhook opt-out (ssl_verify=false)
	}

	return c
}

// clientFor picks the HTTP client honouring the webhook's ssl_verify setting.
func (d *Dispatcher) clientFor(wh *models.Webhook) *http.Client {
	if wh.SSLVerify || d.insecureClient == nil {
		return d.client
	}

	return d.insecureClient
}

// DispatchTo creates a delivery for exactly one webhook and queues it,
// regardless of the webhook's event subscriptions or enabled flag.
func (d *Dispatcher) DispatchTo(ctx context.Context, wh *models.Webhook, eventType string, payload map[string]any) error {
	delivery := newLeasedDelivery(wh.ID, eventType, payload)

	if err := d.db.CreateWebhookDelivery(ctx, delivery); err != nil {
		return fmt.Errorf("create webhook delivery: %w", err)
	}

	if !d.enqueue(delivery) {
		d.log.Warnw("webhook delivery queue full; delivery left pending for retry loop", "webhook_id", wh.ID)
	}

	return nil
}
