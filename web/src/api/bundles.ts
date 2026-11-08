// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'

// Release bundles and promotion pipelines. Every route is admin only and
// keyed by (name, version) / pipeline name.

import type {
  BundlePromotion,
  BundleSigningKey,
  PipelineStage as GeneratedPipelineStage,
  PromotionPipeline,
  ReleaseBundle,
  ReleaseBundleItem,
} from './generated/types.gen'

export type Bundle = ReleaseBundle
export type BundleItem = ReleaseBundleItem
/** A stage; `required_checks` are the bundle check names that must have succeeded before a promotion into it. */
export type PipelineStage = GeneratedPipelineStage & { required_checks?: string[] }
export type Pipeline = Omit<PromotionPipeline, 'stages'> & { stages: PipelineStage[] }
export type { BundlePromotion, BundleSigningKey }

export type BundleCheckStatus = 'pending' | 'success' | 'failure' | 'error'

/** A check reported against a bundle (by a CI pipeline, a policy engine, ...). */
export interface BundleCheck {
  name: string
  status: BundleCheckStatus
  summary: string
  details_url: string
  reported_by: string
  updated_at: string
  /** RFC 3339; a check past it no longer counts. */
  expires_at: string | null
}
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

/** A stored file the item picker offers: the search hit carries everything an item needs. */
export interface ItemCandidate {
  repository: string
  format: string
  name: string
  version: string
  path: string
  sha256: string
  size: number
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

/**
 * The stored GPG keys the current user may seal bundles with (every stored key for an administrator,
 * the granted ones for anyone else; never the configured package-signing key). Empty: no signing.
 */
export async function listBundleSigningKeys(): Promise<BundleSigningKey[]> {
  const res = await request<{ keys?: BundleSigningKey[] }>('/api/v1/bundles/signing-keys')
  return res.keys ?? []
}

/** Seals a draft; `gpgKeyId` (the id of a key from `listBundleSigningKeys`) additionally signs the manifest. */
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

/** Replaces the description and/or the stages of a pipeline (a stage list replaces the old one whole). */
export function updatePipeline(name: string, input: { description?: string; stages?: PipelineStage[] }): Promise<Pipeline> {
  return request<Pipeline>(`/api/v1/pipelines/${enc(name)}`, json('PATCH', input))
}

/** The checks reported against a bundle, in the server's order. */
export async function listBundleChecks(name: string, version: string): Promise<BundleCheck[]> {
  const res = await request<{ checks?: BundleCheck[] }>(`${bundlePath(name, version)}/checks`)
  return res.checks ?? []
}

/** Stored files matching `q` (free text, or `sha256:<digest>`) in the repositories the caller can read. */
export async function searchItemCandidates(q: string, signal?: AbortSignal): Promise<ItemCandidate[]> {
  const qs = new URLSearchParams({ q, limit: '25' })
  const res = await request<{ packages?: ItemCandidate[] }>(`/api/v1/search?${qs}`, { signal })
  return res?.packages ?? []
}
