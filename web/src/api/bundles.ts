// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'

// Release bundles and promotion pipelines. Every route is admin only and
// keyed by (name, version) / pipeline name.

import type {
  BundlePromotion,
  PipelineStage,
  PromotionPipeline,
  ReleaseBundle,
  ReleaseBundleItem,
} from './generated/types.gen'

export type Bundle = ReleaseBundle
export type BundleItem = ReleaseBundleItem
export type Pipeline = PromotionPipeline
export type { BundlePromotion, PipelineStage }
export type BundleState = Bundle['state']
export type PromotionStatus = NonNullable<BundlePromotion['status']>

export interface BundleItemInput {
  source_repo_id: string
  sha256: string
  artifact_path: string
  format?: string
  size_bytes?: number
  component_id?: string
}

interface BundleList {
  total: number
  page: number
  limit: number
  bundles: Bundle[]
}

interface BundleListQuery {
  name?: string
  state?: BundleState | ''
  page?: number
  limit?: number
}

interface PromoteInput {
  to_stage: string
  target_repo: string
  notes?: string
}

const enc = encodeURIComponent

const json = (method: string, body: unknown): RequestInit => ({
  method,
  headers: JSON_HEADERS,
  body: JSON.stringify(body),
})

/** `/api/v1/bundles/{name}/{version}` with both segments escaped. */
function bundlePath(name: string, version: string): string {
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

/** Outcome of a successful promotion. */
export interface PromoteResult {
  promotion: BundlePromotion
  /** Artifacts newly written to the target. */
  copied: number
  /** Artifacts the target already held with the same content. */
  existing: number
  /** Artifacts a forced promotion moved to the target's trash. */
  replaced: number
}

/** `force` (admins) replaces conflicting content in the target instead of answering 409. */
export function promoteBundle(name: string, version: string, input: PromoteInput, force = false): Promise<PromoteResult> {
  const query = force ? '?force=true' : ''
  return request<PromoteResult>(`${bundlePath(name, version)}/promote${query}`, json('POST', input))
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
