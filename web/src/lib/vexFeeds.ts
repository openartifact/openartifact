// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { VexFeed, VexFeedInput } from '../api/vexFeeds'

/** The server's bounds of the sync interval, in hours. */
export const VEX_FEED_INTERVAL_MIN = 1
export const VEX_FEED_INTERVAL_MAX = 720
const VEX_FEED_INTERVAL_DEFAULT = 24
/** The server rejects a larger pinned key ring. */
export const VEX_FEED_KEYS_MAX_BYTES = 64 << 10

/** Same rule as federation peer names. */
const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$/
const LOOPBACK: Record<string, true> = { localhost: true, '127.0.0.1': true, '[::1]': true }

export interface VexFeedForm {
  name: string
  url: string
  enabled: boolean
  requireSignature: boolean
  pgpKeys: string
  /** A number input: '' while empty. */
  intervalHours: number | ''
}

export type VexFeedFormErrors = Partial<Record<'name' | 'url' | 'pgpKeys' | 'intervalHours', 'required' | 'invalid' | 'tooLarge' | 'range'>>

export function emptyVexFeedForm(): VexFeedForm {
  return { name: '', url: '', enabled: true, requireSignature: true, pgpKeys: '', intervalHours: VEX_FEED_INTERVAL_DEFAULT }
}

export function vexFeedFormFrom(feed: VexFeed): VexFeedForm {
  return {
    name: feed.name,
    url: feed.url,
    enabled: feed.enabled,
    requireSignature: feed.require_signature,
    pgpKeys: feed.pgp_keys,
    intervalHours: feed.interval_hours,
  }
}

/** https, or http to a loopback host (a provider on the same machine), as the server accepts. */
function isFeedUrl(raw: string): boolean {
  try {
    const u = new URL(raw)
    if (u.host === '') return false
    return u.protocol === 'https:' || (u.protocol === 'http:' && Object.hasOwn(LOOPBACK, u.hostname))
  } catch {
    return false
  }
}

/** Field error codes of the form; empty when the server's syntactic checks pass. */
export function validateVexFeedForm(form: VexFeedForm): VexFeedFormErrors {
  const errors: VexFeedFormErrors = {}
  const name = form.name.trim()
  const url = form.url.trim()
  if (name === '') errors.name = 'required'
  else if (!NAME_RE.test(name)) errors.name = 'invalid'
  if (url === '') errors.url = 'required'
  else if (!isFeedUrl(url)) errors.url = 'invalid'
  if (new TextEncoder().encode(form.pgpKeys.trim()).length > VEX_FEED_KEYS_MAX_BYTES) errors.pgpKeys = 'tooLarge'
  const h = form.intervalHours
  if (h === '' || !Number.isInteger(h) || h < VEX_FEED_INTERVAL_MIN || h > VEX_FEED_INTERVAL_MAX) errors.intervalHours = 'range'
  return errors
}

/** The POST/PUT body of a valid form (PUT replaces every editable field). */
export function vexFeedBody(form: VexFeedForm): VexFeedInput {
  return {
    name: form.name.trim(),
    url: form.url.trim(),
    enabled: form.enabled,
    require_signature: form.requireSignature,
    pgp_keys: form.pgpKeys.trim(),
    interval_hours: Number(form.intervalHours),
  }
}
