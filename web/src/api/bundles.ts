// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'

// Release bundles and promotion pipelines. Every route is admin only and
// keyed by (name, version) / pipeline name.

export type BundleState = 'draft' | 'sealed' | 'archived'

export interface Bundle {
  id: string
  name: string
  version: string
  description: string
  state: BundleState
  pipeline_id: string | null
  current_stage: string | null
  created_by: string
  sealed_at: string | null
  sealed_by: string | null
  archived_at: string | null
  archived_by: string | null
  /** Fingerprint of the key that signed the manifest when the bundle was sealed; null when unsigned. */
  gpg_key_id?: string | null
  /** Armored detached signature of the manifest; null when unsigned. */
  gpg_signature?: string | null
  created_at: string
  updated_at: string
}

export interface BundleItem {
  id: string
  bundle_id: string
  source_repo_id: string
  component_id: string | null
  sha256: string
  artifact_path: string
  format: string
  size_bytes: number
  added_by: string
  added_at: string
}

export interface BundleItemInput {
  source_repo_id: string
  sha256: string
  artifact_path: string
  format?: string
  size_bytes?: number
  component_id?: string
}

export type PromotionStatus = 'in_progress' | 'success' | 'failed'

export interface BundlePromotion {
  id: string
  bundle_id: string
  from_stage: string | null
  to_stage: string
  target_repo_id: string
  status: PromotionStatus
  promoted_by: string
  notes: string
  error_message: string | null
  item_count: number
  started_at: string
  completed_at: string | null
}

export interface PipelineStage {
  name: string
  repo_id?: string
}

export interface Pipeline {
  id: string
  name: string
  description: string
  stages: PipelineStage[]
  created_by: string
  created_at: string
  updated_at: string
}

export interface BundleList {
  total: number
  page: number
  limit: number
  bundles: Bundle[]
}

export interface BundleListQuery {
  name?: string
  state?: BundleState | ''
  page?: number
  limit?: number
}

export interface PromoteInput {
  to_stage: string
  target_repo: string
  notes?: string
}

const enc = encodeURIComponent

const json = (method: string, body: unknown): RequestInit => ({
  method,
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
})

/** `/api/v1/bundles/{name}/{version}` with both segments escaped. */
export function bundlePath(name: string, version: string): string {
  return `/api/v1/bundles/${enc(name)}/${enc(version)}`
}

export function listBundles(q: BundleListQuery = {}): Promise<BundleList> {
  const qs = new URLSearchParams()
  if (q.name) qs.set('name', q.name)
  if (q.state) qs.set('state', q.state)
  if (q.page != null) qs.set('page', String(q.page))
  if (q.limit != null) qs.set('limit', String(q.limit))
  const s = qs.toString()
  return request<BundleList>(`/api/v1/bundles${s ? `?${s}` : ''}`)
}

export function getBundle(name: string, version: string): Promise<Bundle> {
  return request<Bundle>(bundlePath(name, version))
}

export function createBundle(input: {
  name: string
  version: string
  description?: string
  pipeline_id?: string
}): Promise<Bundle> {
  return request<Bundle>('/api/v1/bundles', json('POST', input))
}

export function deleteBundle(name: string, version: string): Promise<void> {
  return request<void>(bundlePath(name, version), { method: 'DELETE' })
}

export async function listBundleItems(name: string, version: string): Promise<BundleItem[]> {
  const res = await request<{ items?: BundleItem[] }>(`${bundlePath(name, version)}/items`)
  return res.items ?? []
}

export async function addBundleItems(name: string, version: string, items: BundleItemInput[]): Promise<BundleItem[]> {
  const res = await request<{ items?: BundleItem[] }>(`${bundlePath(name, version)}/items`, json('POST', { items }))
  return res.items ?? []
}

export function removeBundleItem(name: string, version: string, sha256: string): Promise<void> {
  return request<void>(`${bundlePath(name, version)}/items/${enc(sha256)}`, { method: 'DELETE' })
}

/** Seals a draft; `gpgKeyId` (a key fingerprint) additionally signs the manifest. */
export function sealBundle(name: string, version: string, gpgKeyId?: string): Promise<Bundle> {
  const init: RequestInit = gpgKeyId ? json('POST', { gpg_key_id: gpgKeyId }) : { method: 'POST' }
  return request<Bundle>(`${bundlePath(name, version)}/seal`, init)
}

export function archiveBundle(name: string, version: string): Promise<Bundle> {
  return request<Bundle>(`${bundlePath(name, version)}/archive`, { method: 'POST' })
}

/** The caller reloads the bundle and its promotions afterwards, so the response body is not used. */
export async function promoteBundle(name: string, version: string, input: PromoteInput): Promise<void> {
  await request<unknown>(`${bundlePath(name, version)}/promote`, json('POST', input))
}

export async function listPromotions(name: string, version: string): Promise<BundlePromotion[]> {
  const res = await request<{ promotions?: BundlePromotion[] }>(`${bundlePath(name, version)}/promotions`)
  return res.promotions ?? []
}

/** URL of the manifest the signature covers (JSON). */
export function manifestUrl(name: string, version: string): string {
  return `${bundlePath(name, version)}/manifest`
}

/** URL of the armored detached signature (404 while the bundle is unsigned). */
export function signatureUrl(name: string, version: string): string {
  return `${bundlePath(name, version)}/manifest.asc`
}

export async function listPipelines(): Promise<Pipeline[]> {
  const res = await request<{ pipelines?: Pipeline[] }>('/api/v1/pipelines')
  return res.pipelines ?? []
}

export function createPipeline(input: {
  name: string
  description?: string
  stages: PipelineStage[]
}): Promise<Pipeline> {
  return request<Pipeline>('/api/v1/pipelines', json('POST', input))
}
