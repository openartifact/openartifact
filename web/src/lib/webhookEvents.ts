// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/** Event names emitted by the backend webhook dispatcher (internal/webhook). */
export const WEBHOOK_EVENTS = [
  'artifact.uploaded',
  'artifact.deleted',
  'repository.created',
  'repository.deleted',
  'artifact.restored',
  'artifact.purged',
  'repository.restored',
  'repository.purged',
] as const

export type WebhookEvent = (typeof WEBHOOK_EVENTS)[number]

/** Delivery body formats a webhook can use (internal/webhook/format.go). */
export const WEBHOOK_PAYLOAD_FORMATS = ['openartifact', 'cloudevents', 'slack', 'teams', 'discord'] as const

export type WebhookPayloadFormat = (typeof WEBHOOK_PAYLOAD_FORMATS)[number]
