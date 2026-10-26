// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { getFederationStatus } from '../api/federation'
import type { FederationPeer } from '../api/generated/types.gen'
import { listAllRepositories } from '../api/repositories'
import { federationAvailable } from './repoTypes'
import { mapPool } from './pool'

export type SyncState = 'none' | 'pending' | 'ok' | 'attention'

export interface FederationRepoRow {
  repository: string
  format: string
  peers: FederationPeer[]
  headSeq: number
  state: SyncState
  /** Peers that are lagging or failing. */
  attention: number
}

export interface FederationOverview {
  /** Whether the server has federation switched on; null when no repository could tell. */
  serverEnabled: boolean | null
  instanceId: string
  rows: FederationRepoRow[]
  /** Federated repositories whose status could not be read. */
  failed: string[]
}

const CONCURRENCY = 6

/** How the peers of one repository are doing, in one word. Peers that are switched off do not count. */
export function syncState(peers: readonly Pick<FederationPeer, 'state'>[]): { state: SyncState; attention: number } {
  const active = peers.filter((p) => p.state !== 'disabled')
  if (active.length === 0) return { state: 'none', attention: 0 }
  const attention = active.filter((p) => p.state === 'error' || p.state === 'lagging').length
  if (attention > 0) return { state: 'attention', attention }
  return { state: active.every((p) => p.state === 'pending') ? 'pending' : 'ok', attention: 0 }
}

/**
 * Federation is configured per server and switched on per repository, so the instance view is gathered:
 * the server flag comes from any federation-capable repository, the rest from the federated ones.
 */
export async function loadFederationOverview(): Promise<FederationOverview> {
  const repos = await listAllRepositories()
  const federated = repos.filter((r) => r.federated === true)
  const probe = federated[0] ?? repos.find((r) => federationAvailable(r.repo_type, r.format))
  if (!probe) return { serverEnabled: null, instanceId: '', rows: [], failed: [] }

  const first = await getFederationStatus(probe.name)
  if (!first.server_enabled) return { serverEnabled: false, instanceId: first.instance_id, rows: [], failed: [] }

  const results = await mapPool(federated, CONCURRENCY, (r) => (r.name === probe.name ? Promise.resolve(first) : getFederationStatus(r.name)))
  const rows: FederationRepoRow[] = []
  const failed: string[] = []
  results.forEach((res, i) => {
    const repo = federated[i]
    if (!res.ok) {
      failed.push(repo.name)
      return
    }
    const peers = res.value.peers ?? []
    rows.push({ repository: repo.name, format: repo.format, peers, headSeq: res.value.head_seq, ...syncState(peers) })
  })
  return { serverEnabled: true, instanceId: first.instance_id, rows, failed }
}
