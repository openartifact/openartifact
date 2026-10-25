// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'

export interface Webhook {
  id: string
  repository_id?: string
  name: string
  url: string
  has_secret?: boolean
  events: string[]
  content_type?: string
  payload_format?: string
  ssl_verify?: boolean
  enabled: boolean
  max_retries?: number
  timeout_seconds?: number
  created_at?: string
  updated_at?: string
}

interface WebhookListResponse {
  webhooks?: Webhook[]
}

/** Body accepted by {@link createWebhook}; the hook is always created enabled with TLS verification on. */
export interface WebhookCreateInput {
  name: string
  url: string
  events: string[]
  payload_format: string
  ssl_verify: boolean
  secret?: string
}

/** Flags applied by {@link updateWebhook}. */
export interface WebhookUpdateInput {
  enabled: boolean
  ssl_verify: boolean
}

const BASE = '/api/v1/webhooks'
const enc = encodeURIComponent

export async function listWebhooks(): Promise<Webhook[]> {
  const res = await request<WebhookListResponse>(BASE)
  return res?.webhooks ?? []
}

export function createWebhook(body: WebhookCreateInput): Promise<Webhook> {
  return request<Webhook>(BASE, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export function updateWebhook(id: string, body: WebhookUpdateInput): Promise<Webhook> {
  return request<Webhook>(`${BASE}/${enc(id)}`, { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export async function deleteWebhook(id: string): Promise<void> {
  await request(`${BASE}/${enc(id)}`, { method: 'DELETE' })
}
