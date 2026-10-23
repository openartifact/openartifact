// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'
import type {
  DependencyTrackSettings,
  DependencyTrackRepository,
  DependencyTrackPush,
  DependencyTrackTest,
} from './generated/types.gen'

export type { DependencyTrackSettings, DependencyTrackRepository, DependencyTrackPush, DependencyTrackTest }

/** Settings body: `api_key` omitted keeps the stored key, `""` clears it. */
export interface DependencyTrackSettingsInput {
  enabled: boolean
  base_url: string
  api_key?: string
  project_name: string
  project_version: string
  auto_push: boolean
  push_vex: boolean
}

/** Override body: empty strings and `null` inherit the global value. */
export interface DependencyTrackOverrideInput {
  enabled: boolean
  base_url: string
  api_key?: string
  project_name: string
  project_version: string
  auto_push: boolean | null
  push_vex: boolean | null
}

const JSON_HEADERS = { 'Content-Type': 'application/json' }

function repoPath(repo: string): string {
  return `/api/v1/repositories/${encodeURIComponent(repo)}/dependency-track`
}

export function getDependencyTrackSettings(): Promise<DependencyTrackSettings> {
  return request<DependencyTrackSettings>('/api/v1/admin/dependency-track')
}

export function saveDependencyTrackSettings(body: DependencyTrackSettingsInput): Promise<DependencyTrackSettings> {
  return request<DependencyTrackSettings>('/api/v1/admin/dependency-track', {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export function testDependencyTrack(): Promise<DependencyTrackTest> {
  return request<DependencyTrackTest>('/api/v1/admin/dependency-track/test', { method: 'POST' })
}

export function getRepoDependencyTrack(repo: string): Promise<DependencyTrackRepository> {
  return request<DependencyTrackRepository>(repoPath(repo))
}

export function saveRepoDependencyTrack(repo: string, body: DependencyTrackOverrideInput): Promise<DependencyTrackRepository> {
  return request<DependencyTrackRepository>(repoPath(repo), {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  })
}

export function deleteRepoDependencyTrack(repo: string): Promise<void> {
  return request<void>(repoPath(repo), { method: 'DELETE' })
}

export function testRepoDependencyTrack(repo: string): Promise<DependencyTrackTest> {
  return request<DependencyTrackTest>(`${repoPath(repo)}/test`, { method: 'POST' })
}

export function pushRepoDependencyTrack(repo: string, artifactId: string): Promise<DependencyTrackPush> {
  return request<DependencyTrackPush>(`${repoPath(repo)}/push`, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify({ artifact_id: artifactId }),
  })
}

export async function listRepoDependencyTrackPushes(repo: string): Promise<DependencyTrackPush[]> {
  const res = await request<{ pushes?: DependencyTrackPush[] }>(`${repoPath(repo)}/pushes`)
  return res.pushes ?? []
}
