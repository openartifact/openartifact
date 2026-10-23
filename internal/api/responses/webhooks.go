// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package responses

// WebhookResponse represents a webhook in API responses. The signing secret is
// write-only: it is never returned, only whether one is configured.
type WebhookResponse struct {
	ID             string   `json:"id"`
	RepositoryID   *string  `json:"repository_id"`
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Enabled        bool     `json:"enabled"`
	Events         []string `json:"events"`
	ContentType    string   `json:"content_type"`
	PayloadFormat  string   `json:"payload_format"`
	HasSecret      bool     `json:"has_secret"`
	SSLVerify      bool     `json:"ssl_verify"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxRetries     int      `json:"max_retries"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}
