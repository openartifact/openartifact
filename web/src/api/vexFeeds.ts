// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import type { VexFeed, VexFeedRequest } from './generated/types.gen'

// Vendor CSAF VEX feeds are global and admin-only. A sync runs in the
// background; its outcome shows up in the feed's last_* fields.

export type { VexFeed }

/** What POST and PUT accept: PUT replaces every editable field. */
export type VexFeedInput = VexFeedRequest

const BASE = '/api/v1/vex/feeds'
const one = (id: string): string => `${BASE}/${encodeURIComponent(id)}`

export async function listVexFeeds(): Promise<VexFeed[]> {
  const res = await request<{ feeds?: VexFeed[] }>(BASE)
  return res.feeds ?? []
}

export function createVexFeed(body: VexFeedInput): Promise<VexFeed> {
  return request<VexFeed>(BASE, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export function updateVexFeed(id: string, body: VexFeedInput): Promise<VexFeed> {
  return request<VexFeed>(one(id), { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

/** Removes the feed and the decisions it materialized. */
export function deleteVexFeed(id: string): Promise<void> {
  return request<void>(one(id), { method: 'DELETE' })
}

/** Starts a sync in the background (202); a sync already running is a 409 `conflict` ApiError. */
export async function syncVexFeed(id: string): Promise<void> {
  await request<{ status?: string }>(`${one(id)}/sync`, { method: 'POST' })
}
