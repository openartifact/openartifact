// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { listCleanupPolicies } from '../api/cleanup'
import { listAllRepositories } from '../api/repositories'
import { cleanupPolicyFromApi, type CleanupPolicyView } from './cleanupPolicy'
import { mapPool } from './pool'

/** A repository cleanup policy with the repository it belongs to. */
export interface CleanupOverviewRow extends CleanupPolicyView {
  repository: string
  format: string
}

export interface CleanupOverview {
  rows: CleanupOverviewRow[]
  /** Hosted repositories whose policies could not be read. */
  failed: string[]
}

const CONCURRENCY = 6

/** Policies live on repositories: gathers them from every hosted repository. */
export async function loadCleanupOverview(): Promise<CleanupOverview> {
  const repos = (await listAllRepositories()).filter((r) => r.repo_type === 'hosted' || r.repo_type === undefined)
  const results = await mapPool(repos, CONCURRENCY, (r) => listCleanupPolicies(r.name))
  const rows: CleanupOverviewRow[] = []
  const failed: string[] = []
  results.forEach((res, i) => {
    const repo = repos[i]
    if (!res.ok) {
      failed.push(repo.name)
      return
    }
    for (const p of res.value) rows.push({ ...cleanupPolicyFromApi(p), repository: repo.name, format: repo.format })
  })
  return { rows, failed }
}
