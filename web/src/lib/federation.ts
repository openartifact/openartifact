// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t, tDynamic } from '../i18n/en'
import { formatDuration } from './duration'
import { isFederationFormat } from './repoTypes'
import type { Repository } from '../api/repositories'
import type {
  CreateFederationPeerRequest,
  FederationPeer,
  FederationPeerState,
  UpdateFederationPeerRequest,
} from '../api/federation'

type StateVariant = 'success' | 'warning' | 'neutral' | 'danger'

/** Poll period of the status endpoint while the tab is visible. */
export const FEDERATION_POLL_MS = 10_000

const STATE_VARIANTS: Record<FederationPeerState, StateVariant> = {
  ok: 'success',
  lagging: 'warning',
  error: 'danger',
  pending: 'neutral',
  disabled: 'neutral',
}

export function peerStateVariant(state: FederationPeerState): StateVariant {
  return STATE_VARIANTS[state] ?? 'neutral'
}

export function peerStateLabel(state: FederationPeerState): string {
  return tDynamic(`federation.state.${state}`)
}

/** Lag as a short duration with at most two units ("1 hr 5 min"); "Caught up" when below one second. */
export function formatLag(seconds: number | null | undefined): string {
  const rest = Math.floor(seconds ?? 0)
  if (!Number.isFinite(rest) || rest < 1) return t('federation.lag.none')
  return formatDuration(rest, { display: 'short', maxUnits: 2 })
}

const ERROR_PREVIEW_CHARS = 80

/** Cuts `text` to `max` characters with an ellipsis; `truncated` says whether anything was dropped. */
export function previewError(text: string | null | undefined, max = ERROR_PREVIEW_CHARS): { text: string; truncated: boolean } {
  const s = (text ?? '').trim()
  if (s.length <= max) return { text: s, truncated: false }
  return { text: `${s.slice(0, max).trimEnd()}…`, truncated: true }
}

/** Substitutes `{key}` placeholders of a catalog string. */
export function fill(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (m, key: string) => (key in values ? String(values[key]) : m))
}

export interface PeerForm {
  name: string
  baseUrl: string
  token: string
  caPem: string
  clientCert: string
  clientKey: string
  enabled: boolean
}

export function emptyPeerForm(): PeerForm {
  return { name: '', baseUrl: '', token: '', caPem: '', clientCert: '', clientKey: '', enabled: true }
}

/** Edit form seeded from a peer; secrets are never returned, so they start empty. */
export function peerFormFrom(peer: FederationPeer): PeerForm {
  return { ...emptyPeerForm(), name: peer.name, baseUrl: peer.base_url, enabled: peer.enabled }
}

export interface PeerFormErrors {
  name?: string
  baseUrl?: string
  token?: string
  clientCert?: string
}

function isHttpUrl(raw: string): boolean {
  try {
    const u = new URL(raw)
    return (u.protocol === 'http:' || u.protocol === 'https:') && u.host !== ''
  } catch {
    return false
  }
}

/**
 * Field errors of the peer form; empty when valid. The token is required to add a peer and
 * optional when editing (empty keeps the stored one). A client certificate needs its key and vice versa.
 */
export function validatePeerForm(form: PeerForm, mode: 'create' | 'edit'): PeerFormErrors {
  const errors: PeerFormErrors = {}
  if (form.name.trim() === '') errors.name = t('federation.peerDialog.nameRequired')
  if (!isHttpUrl(form.baseUrl.trim())) errors.baseUrl = t('federation.peerDialog.urlInvalid')
  if (mode === 'create' && form.token.trim() === '') errors.token = t('federation.peerDialog.tokenRequired')
  if ((form.clientCert.trim() === '') !== (form.clientKey.trim() === '')) {
    errors.clientCert = t('federation.peerDialog.clientPair')
  }
  return errors
}

export function peerFormValid(form: PeerForm, mode: 'create' | 'edit'): boolean {
  return Object.keys(validatePeerForm(form, mode)).length === 0
}

/** Request body for a new peer; optional TLS fields are sent only when filled in. */
export function peerCreateBody(form: PeerForm): CreateFederationPeerRequest {
  const body: CreateFederationPeerRequest = {
    name: form.name.trim(),
    base_url: form.baseUrl.trim(),
    token: form.token.trim(),
  }
  if (form.caPem.trim()) body.ca_pem = form.caPem.trim()
  if (form.clientCert.trim()) body.client_cert = form.clientCert.trim()
  if (form.clientKey.trim()) body.client_key = form.clientKey.trim()
  return body
}

/** PATCH body holding only what changed against `peer`; secrets are sent only when newly entered. */
export function peerUpdateBody(form: PeerForm, peer: FederationPeer): UpdateFederationPeerRequest {
  const body: UpdateFederationPeerRequest = {}
  if (form.name.trim() !== peer.name) body.name = form.name.trim()
  if (form.baseUrl.trim() !== peer.base_url) body.base_url = form.baseUrl.trim()
  if (form.enabled !== peer.enabled) body.enabled = form.enabled
  if (form.token.trim()) body.token = form.token.trim()
  if (form.caPem.trim()) body.ca_pem = form.caPem.trim()
  if (form.clientCert.trim()) body.client_cert = form.clientCert.trim()
  if (form.clientKey.trim()) body.client_key = form.clientKey.trim()
  return body
}

/**
 * Whether the repository page offers the Federation tab: repositories of a federation-capable
 * format (proxy and group ones get the "not available" explanation) and any repository that is federated.
 */
export function showsFederation(repo: Pick<Repository, 'format' | 'federated'>): boolean {
  return repo.federated === true || isFederationFormat(repo.format)
}

/** True once a resync was requested and the peer has not picked it up yet. */
export function resyncPending(peer: Pick<FederationPeer, 'resync_requested'>, busy: boolean): boolean {
  return busy || peer.resync_requested
}

/** Replaces the peer with the same id (or appends it); returns a new list. */
export function upsertPeer(peers: readonly FederationPeer[], peer: FederationPeer): FederationPeer[] {
  const i = peers.findIndex((p) => p.id === peer.id)
  if (i < 0) return [...peers, peer]
  const next = [...peers]
  next[i] = peer
  return next
}
