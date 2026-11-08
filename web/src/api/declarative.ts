// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ApiError, JSON_HEADERS, request, requestText } from './client'
import type { DeclarativeObject, DeclarativeObjectList, DeclarativeRun } from './generated/types.gen'

export type { DeclarativeObject, DeclarativeRun }
export type { DeclarativeRunItem, DeclarativeRunProblem, DeclarativeRunSecret } from './generated/types.gen'

/** The manifest kinds `oa apply` manages (docs/integrations/declarative.md), in the order the engine applies them. */
export const DECLARATIVE_KINDS = [
  'User',
  'ServiceAccount',
  'Group',
  'Project',
  'Repository',
  'PermissionTarget',
  'OIDCTrustPolicy',
  'SSOProvider',
  'Webhook',
  'StorageTier',
  'TierPolicy',
] as const

export interface DeclarativeRequest {
  /** The YAML stream, as `oa apply -f -` reads it. */
  manifest: string
  /** Only these kinds; empty means all. */
  kinds?: string[]
  prune?: boolean
  prune_all?: boolean
  force_secrets?: boolean
  continue_on_error?: boolean
  /** Values of the `secretRef: {env: NAME}` references, by NAME. */
  secrets?: Record<string, string>
}

const post = (body: DeclarativeRequest): RequestInit => ({
  method: 'POST',
  headers: JSON_HEADERS,
  body: JSON.stringify(body),
})

/** The plan for a manifest; changes nothing. A refusal (invalid manifest) is an ApiError: read it with {@link declarativeRefusal}. */
export function diffManifest(req: DeclarativeRequest): Promise<DeclarativeRun> {
  return request<DeclarativeRun>('/api/v1/admin/declarative/diff', post(req))
}

/** Applies a manifest and answers the plan with the outcome of each item. */
export function applyManifest(req: DeclarativeRequest): Promise<DeclarativeRun> {
  return request<DeclarativeRun>('/api/v1/admin/declarative/apply', post(req))
}

/**
 * The plan or manifest problems a refused diff/apply carries (422), or null for any other failure.
 * The refusal has the shape of a run: `code`, `error`, and `problems` or the plan. A refusal that
 * carries no plan (an older server, an unforeseen path) still gets an empty `items`, so the plan view can render it.
 */
export function declarativeRefusal(err: unknown): DeclarativeRun | null {
  if (!(err instanceof ApiError) || err.status !== 422 || typeof err.body !== 'object' || err.body === null) return null
  const body = err.body as DeclarativeRun
  return { ...body, items: body.items ?? [] }
}

function exportQuery(kinds: readonly string[], withMembers: boolean): string {
  const q = new URLSearchParams()
  for (const kind of kinds) q.append('kind', kind)
  if (withMembers) q.set('with_members', 'true')
  const s = q.toString()
  return s ? `?${s}` : ''
}

/** URL of the YAML export of the live state (an attachment). */
export function declarativeExportUrl(kinds: readonly string[], withMembers: boolean): string {
  return `/api/v1/admin/declarative/export${exportQuery(kinds, withMembers)}`
}

/** The live state as a manifest stream, as text. */
export function exportManifest(kinds: readonly string[], withMembers: boolean): Promise<string> {
  return requestText(declarativeExportUrl(kinds, withMembers))
}

/** The objects `oa apply` created or adopted, and so owns. */
export async function listManaged(): Promise<DeclarativeObject[]> {
  const res = await request<DeclarativeObjectList>('/api/v1/admin/declarative')
  return res.items ?? []
}

/** Stops managing one object (the object itself stays): `oa apply --prune` no longer deletes it. */
export function releaseManaged(kind: string, name: string): Promise<void> {
  const q = new URLSearchParams({ kind, name })
  return request<void>(`/api/v1/admin/declarative?${q}`, { method: 'DELETE' })
}
