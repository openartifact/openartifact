// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/** Event names emitted by the backend webhook dispatcher (internal/webhook). */
export const WEBHOOK_EVENTS = [
  'artifact.uploaded',
  'artifact.deleted',
  'artifact.downloaded',
  'repository.created',
  'repository.deleted',
] as const

export type WebhookEvent = (typeof WEBHOOK_EVENTS)[number]
