// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Webhook payload formats (webhooks.payload_format).
const (
	PayloadFormatOpenArtifact = "openartifact"
	PayloadFormatCloudEvents  = "cloudevents"
	PayloadFormatSlack        = "slack"
	PayloadFormatTeams        = "teams"
	PayloadFormatDiscord      = "discord"
)

type Webhook struct {
	ID             string    `json:"id"`
	RepositoryID   string    `json:"repository_id"` // empty = global
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	SecretEnc      string    `json:"-"` // AES-256-GCM encrypted HMAC signing secret (internal/crypto); never serialised
	Enabled        bool      `json:"enabled"`
	Events         []string  `json:"events"` // ["artifact.uploaded", "artifact.deleted", ...]
	ContentType    string    `json:"content_type"`
	PayloadFormat  string    `json:"payload_format"` // one of the PayloadFormat* constants; empty (older backups) means openartifact
	SSLVerify      bool      `json:"ssl_verify"`
	MaxRetries     int       `json:"max_retries"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type WebhookDelivery struct {
	ID             string         `json:"id"`
	WebhookID      string         `json:"webhook_id"`
	EventType      string         `json:"event_type"`
	Payload        map[string]any `json:"payload"`
	Status         string         `json:"status"` // "pending", "success", "failed"
	ResponseStatus int            `json:"response_status"`
	ResponseBody   string         `json:"response_body"`
	AttemptCount   int            `json:"attempt_count"`
	NextRetryAt    *time.Time     `json:"next_retry_at"`
	DeliveredAt    *time.Time     `json:"delivered_at"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}
