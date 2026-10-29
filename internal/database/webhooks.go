// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// WebhookStore owns webhooks and their deliveries.
type WebhookStore struct{ *core }

// CreateWebhook creates a new webhook.
func (p *WebhookStore) CreateWebhook(ctx context.Context, wh *models.Webhook) error {
	// Convert repoID to uuid.NullUUID
	var repoID *string

	if wh.RepositoryID != "" {
		repoID = &wh.RepositoryID
	}

	row, err := p.q.CreateWebhook(ctx, dbsqlc.CreateWebhookParams{
		RepositoryID:   repoID,
		Name:           wh.Name,
		Url:            wh.URL,
		SecretEnc:      strPtr(wh.SecretEnc),
		Enabled:        wh.Enabled,
		Events:         wh.Events,
		ContentType:    wh.ContentType,
		PayloadFormat:  cmp.Or(wh.PayloadFormat, models.PayloadFormatOpenArtifact),
		SslVerify:      wh.SSLVerify,
		MaxRetries:     int32(wh.MaxRetries),     //nolint:gosec // G115: value is bounded by API validation
		TimeoutSeconds: int32(wh.TimeoutSeconds), //nolint:gosec // G115: value is bounded by API validation
	})

	return finishCreate(insertedRow(row), err, "create webhook", &wh.ID, &wh.CreatedAt, &wh.UpdatedAt)
}

// GetWebhook retrieves a webhook by ID.
// webhookFromRow maps a sqlc webhook row to the model. The list row types share
// GetWebhookRow's layout and are converted to it by the callers.
func webhookFromRow(r dbsqlc.GetWebhookRow) *models.Webhook {
	repoID := ""

	if r.RepositoryID != nil {
		if s, ok := r.RepositoryID.(string); ok {
			repoID = s
		}
	}

	return &models.Webhook{
		ID:             r.ID,
		RepositoryID:   repoID,
		Name:           r.Name,
		URL:            r.Url,
		SecretEnc:      r.SecretEnc,
		Enabled:        r.Enabled,
		Events:         r.Events,
		ContentType:    r.ContentType,
		PayloadFormat:  r.PayloadFormat,
		SSLVerify:      r.SslVerify,
		MaxRetries:     int(r.MaxRetries),
		TimeoutSeconds: int(r.TimeoutSeconds),
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

func (p *WebhookStore) GetWebhook(ctx context.Context, id string) (*models.Webhook, error) {
	row, err := p.q.GetWebhook(ctx, id)

	return mapRow(row, err, "get webhook", webhookFromRow)
}

// UpdateWebhook updates an existing webhook.
func (p *WebhookStore) UpdateWebhook(ctx context.Context, wh *models.Webhook) error {
	err := p.q.UpdateWebhook(ctx, dbsqlc.UpdateWebhookParams{
		ID:             wh.ID,
		Name:           wh.Name,
		Url:            wh.URL,
		SecretEnc:      strPtr(wh.SecretEnc),
		Enabled:        wh.Enabled,
		Events:         wh.Events,
		ContentType:    wh.ContentType,
		PayloadFormat:  cmp.Or(wh.PayloadFormat, models.PayloadFormatOpenArtifact),
		SslVerify:      wh.SSLVerify,
		MaxRetries:     int32(wh.MaxRetries),     //nolint:gosec // G115: value is bounded by API validation
		TimeoutSeconds: int32(wh.TimeoutSeconds), //nolint:gosec // G115: value is bounded by API validation
	})
	if err != nil {
		return fmt.Errorf("failed to update webhook: %w", err)
	}

	return nil
}

// DeleteWebhook deletes a webhook.
func (p *WebhookStore) DeleteWebhook(ctx context.Context, id string) error {
	err := p.q.DeleteWebhook(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete webhook: %w", err)
	}

	return nil
}

// ListWebhooks lists all webhooks for a repository (or global if repoID is empty).
func (p *WebhookStore) ListWebhooks(ctx context.Context, repoID string) ([]*models.Webhook, error) {
	var webhooks []*models.Webhook

	if repoID == "" {
		rows, err := p.q.ListGlobalWebhooks(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list webhooks: %w", err)
		}

		for _, r := range rows {
			webhooks = append(webhooks, webhookFromRow(dbsqlc.GetWebhookRow(r)))
		}
	} else {
		rows, err := p.q.ListWebhooksByRepo(ctx, &repoID)
		if err != nil {
			return nil, fmt.Errorf("failed to list webhooks: %w", err)
		}

		for _, r := range rows {
			webhooks = append(webhooks, webhookFromRow(dbsqlc.GetWebhookRow(r)))
		}
	}

	return webhooks, nil
}

// ListWebhooksForEvent lists all enabled webhooks that are subscribed to an event.
func (p *WebhookStore) ListWebhooksForEvent(ctx context.Context, repoID, eventType string) ([]*models.Webhook, error) {
	var repoIDPtr *string
	if repoID != "" {
		repoIDPtr = &repoID
	}

	rows, err := p.q.ListWebhooksForEvent(ctx, dbsqlc.ListWebhooksForEventParams{
		RepositoryID: repoIDPtr,
		EventType:    eventType,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list webhooks for event: %w", err)
	}

	var webhooks []*models.Webhook

	for _, r := range rows {
		webhooks = append(webhooks, webhookFromRow(dbsqlc.GetWebhookRow(r)))
	}

	return webhooks, nil
}

// webhookDeliveryToModel converts a dbsqlc.WebhookDelivery to *models.WebhookDelivery.
func webhookDeliveryToModel(row dbsqlc.WebhookDelivery) (*models.WebhookDelivery, error) {
	var payload map[string]any
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal webhook payload: %w", err)
	}

	responseStatus := 0
	if row.ResponseStatus != nil {
		responseStatus = int(*row.ResponseStatus)
	}

	responseBody := ""
	if row.ResponseBody != nil {
		responseBody = *row.ResponseBody
	}

	return &models.WebhookDelivery{
		ID:             row.ID,
		WebhookID:      row.WebhookID,
		EventType:      row.EventType,
		Payload:        payload,
		Status:         row.Status,
		ResponseStatus: responseStatus,
		ResponseBody:   responseBody,
		AttemptCount:   int(row.AttemptCount),
		NextRetryAt:    row.NextRetryAt,
		DeliveredAt:    row.DeliveredAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}, nil
}

// CreateWebhookDelivery creates a new webhook delivery record.
func (p *WebhookStore) CreateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error {
	payloadJSON, err := json.Marshal(delivery.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	row, err := p.q.CreateWebhookDelivery(ctx, dbsqlc.CreateWebhookDeliveryParams{
		WebhookID:    delivery.WebhookID,
		EventType:    delivery.EventType,
		Payload:      payloadJSON,
		Status:       delivery.Status,
		AttemptCount: int32(delivery.AttemptCount), //nolint:gosec // G115: bounded by retry logic
		NextRetryAt:  delivery.NextRetryAt,
	})

	return finishCreate(insertedRow(row), err, "create webhook delivery", &delivery.ID, &delivery.CreatedAt, &delivery.UpdatedAt)
}

// UpdateWebhookDelivery updates a webhook delivery record.
func (p *WebhookStore) UpdateWebhookDelivery(ctx context.Context, delivery *models.WebhookDelivery) error {
	payloadJSON, err := json.Marshal(delivery.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	var responseStatus *int32
	if delivery.ResponseStatus != 0 {
		rs := int32(delivery.ResponseStatus) //nolint:gosec // G115: HTTP status code is bounded
		responseStatus = &rs
	}

	if err := p.q.UpdateWebhookDelivery(ctx, dbsqlc.UpdateWebhookDeliveryParams{
		ID:             delivery.ID,
		Status:         delivery.Status,
		ResponseStatus: responseStatus,
		ResponseBody:   strPtr(delivery.ResponseBody),
		AttemptCount:   int32(delivery.AttemptCount), //nolint:gosec // G115: bounded by retry logic
		NextRetryAt:    delivery.NextRetryAt,
		DeliveredAt:    delivery.DeliveredAt,
		Payload:        payloadJSON,
	}); err != nil {
		return fmt.Errorf("failed to update webhook delivery: %w", err)
	}

	return nil
}

// ListWebhookDeliveries lists deliveries for a webhook.
func (p *WebhookStore) ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]*models.WebhookDelivery, error) {
	rows, err := p.q.ListWebhookDeliveries(ctx, dbsqlc.ListWebhookDeliveriesParams{
		WebhookID: webhookID,
		Limit:     toInt32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list webhook deliveries: %w", err)
	}

	deliveries := make([]*models.WebhookDelivery, 0, len(rows))
	for _, row := range rows {
		d, err := webhookDeliveryToModel(row)
		if err != nil {
			return nil, err
		}

		deliveries = append(deliveries, d)
	}

	return deliveries, nil
}

// ClaimPendingWebhookDeliveries atomically claims up to limit due deliveries
// (pending, next_retry_at unset or elapsed) for lease: it pushes their
// next_retry_at to the end of the lease under FOR UPDATE SKIP LOCKED, so
// concurrent pollers on any number of server instances never receive the same
// row. A claimed delivery that is never completed becomes claimable again once
// the lease expires.
func (p *WebhookStore) ClaimPendingWebhookDeliveries(
	ctx context.Context,
	limit int,
	lease time.Duration,
) ([]*models.WebhookDelivery, error) {
	rows, err := p.q.ClaimPendingWebhookDeliveries(ctx, dbsqlc.ClaimPendingWebhookDeliveriesParams{
		LeaseSeconds: lease.Seconds(),
		BatchSize:    toInt32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to claim pending webhook deliveries: %w", err)
	}

	deliveries := make([]*models.WebhookDelivery, 0, len(rows))
	for _, row := range rows {
		d, err := webhookDeliveryToModel(row)
		if err != nil {
			return nil, err
		}

		deliveries = append(deliveries, d)
	}

	return deliveries, nil
}
