// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import type {
  CreateFederationPeerRequest,
  FederationDeferredUnit,
  FederationPeer,
  FederationStatus,
  FederationToken,
  FederationTokenCreated,
  UpdateFederationPeerRequest,
} from './generated/types.gen'

export type {
  CreateFederationPeerRequest,
  FederationDeferredUnit,
  FederationPeer,
  FederationStatus,
  FederationToken,
  FederationTokenCreated,
  UpdateFederationPeerRequest,
}
export type FederationPeerState = FederationPeer['state']
export type FederationVexTrust = NonNullable<FederationPeer['vex_trust']>

/** What is done with a peer's VEX decisions, in the order they are offered. */
export const FEDERATION_VEX_TRUSTS: readonly FederationVexTrust[] = ['ignore', 'suggest', 'apply']

const enc = encodeURIComponent
function base(repo: string): string {
  return `/api/v1/repositories/${enc(repo)}/federation`
}

export function getFederationStatus(repo: string): Promise<FederationStatus> {
  return request<FederationStatus>(base(repo))
}

/** Enabling backfills the change log; disabling removes the log and every peer. */
export function setFederationEnabled(repo: string, enabled: boolean): Promise<FederationStatus> {
  return request<FederationStatus>(base(repo), {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify({ enabled }),
  })
}

/** Opts the federated repository in (or out) of serving its local VEX decisions to its peers. */
export function setFederationVexPublish(repo: string, vexPublish: boolean): Promise<FederationStatus> {
  return request<FederationStatus>(base(repo), {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify({ enabled: true, vex_publish: vexPublish }),
  })
}

export function createFederationPeer(repo: string, body: CreateFederationPeerRequest): Promise<FederationPeer> {
  return request<FederationPeer>(`${base(repo)}/peers`, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export function updateFederationPeer(repo: string, id: string, body: UpdateFederationPeerRequest): Promise<FederationPeer> {
  return request<FederationPeer>(`${base(repo)}/peers/${enc(id)}`, {
    method: 'PATCH',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export function deleteFederationPeer(repo: string, id: string): Promise<void> {
  return request<void>(`${base(repo)}/peers/${enc(id)}`, { method: 'DELETE' })
}

/** Resets the peer's cursor; the next pass re-reads its whole log and merges idempotently. */
export function resyncFederationPeer(repo: string, id: string): Promise<FederationPeer> {
  return request<FederationPeer>(`${base(repo)}/peers/${enc(id)}/resync`, { method: 'POST' })
}

export async function listFederationDeferred(repo: string, id: string): Promise<FederationDeferredUnit[]> {
  const res = await request<{ items?: FederationDeferredUnit[] }>(`${base(repo)}/peers/${enc(id)}/deferred`)
  return res.items ?? []
}

/** The clear-text token is only in this response. */
export function createFederationToken(repo: string, name: string): Promise<FederationTokenCreated> {
  return request<FederationTokenCreated>(`${base(repo)}/tokens`, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify({ name }),
  })
}

export function revokeFederationToken(repo: string, id: string): Promise<void> {
  return request<void>(`${base(repo)}/tokens/${enc(id)}`, { method: 'DELETE' })
}
