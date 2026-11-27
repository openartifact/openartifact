// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { JSON_HEADERS, request } from './client'
import { getRepository, listAllRepositories } from './repositories'
import type { CopyItem, CopyRequest, CopyResult } from './generated/types.gen'

export type { CopyItem, CopyResult }
export type TransferOperation = 'copy' | 'move'

const enc = encodeURIComponent

/** Copies or moves `items` of the hosted repository `source` into `target`; `dryRun` reports without writing. */
export function transferArtifacts(op: TransferOperation, source: string, target: string, items: CopyItem[], dryRun: boolean): Promise<CopyResult> {
  const body: CopyRequest = { target, items, dry_run: dryRun }
  return request<CopyResult>(`/api/v1/repositories/${enc(source)}/${op}`, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

/** Names of the hosted repositories of `format`, other than `source`, the caller may write. */
export async function listTransferTargets(format: string, source: string, isAdmin: boolean): Promise<string[]> {
  const repos = (await listAllRepositories({ type: 'hosted', format })).filter((r) => r.name !== source)
  if (isAdmin) return repos.map((r) => r.name)
  // The list does not carry the caller's permissions: ask per repository.
  const checked = await Promise.all(repos.slice(0, 100).map((r) => getRepository(r.name).catch(() => null)))
  return checked.filter((r) => r?.permissions?.write === true).map((r) => r!.name)
}
