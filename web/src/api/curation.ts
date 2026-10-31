// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'
import { updateRepository, type Repository } from './repositories'

import type { AvailabilityReason, AvailabilityResponse, CurationRule, HeldBackResponse, HeldBackVersion } from './generated/types.gen'

export type { AvailabilityReason, AvailabilityResponse, CurationRule, HeldBackResponse, HeldBackVersion }
export type CurationAction = CurationRule['action']
/** How a proxy repository's curation rules apply: not at all, everything but the blocked versions, or only the allowed versions. */
export type CurationMode = NonNullable<Repository['curation_mode']>

/** The fields of a rule accepted on create; an update takes any subset of them. */
export interface CurationRuleInput {
  action: CurationAction
  package_pattern: string
  version_pattern?: string
  reason?: string
}

/** A repository response of a proxy repository carries its curation mode. */
export type CurationRepository = Repository

const baseUrl = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}/curation-rules`
const ruleUrl = (repo: string, id: string): string => `${baseUrl(repo)}/${encodeURIComponent(id)}`

export async function listCurationRules(repo: string): Promise<CurationRule[]> {
  const res = await request<{ rules: CurationRule[] }>(baseUrl(repo))
  return res?.rules ?? []
}

export function createCurationRule(repo: string, body: CurationRuleInput): Promise<CurationRule> {
  return request<CurationRule>(baseUrl(repo), { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export function updateCurationRule(repo: string, id: string, body: Partial<CurationRuleInput>): Promise<CurationRule> {
  return request<CurationRule>(ruleUrl(repo, id), { method: 'PATCH', headers: JSON_HEADERS, body: JSON.stringify(body) })
}

export async function deleteCurationRule(repo: string, id: string): Promise<void> {
  await request(ruleUrl(repo, id), { method: 'DELETE' })
}

/** Switches the repository's curation mode; the answer is the updated repository. */
export function setCurationMode(repo: string, mode: CurationMode): Promise<CurationRepository> {
  return updateRepository(repo, { curation_mode: mode })
}

/** Why one package version of a repository is, or is not, available to its clients. */
export function getAvailability(repo: string, pkg: string, version: string): Promise<AvailabilityResponse> {
  const qs = new URLSearchParams({ package: pkg, version })
  return request<AvailabilityResponse>(`/api/v1/repositories/${encodeURIComponent(repo)}/availability?${qs}`)
}

/** The versions of a proxy package its gate holds back, from the metadata already cached. */
export function listHeldBackVersions(repo: string, pkg: string): Promise<HeldBackResponse> {
  const qs = new URLSearchParams({ package: pkg })
  return request<HeldBackResponse>(`/api/v1/repositories/${encodeURIComponent(repo)}/held-back-versions?${qs}`)
}
