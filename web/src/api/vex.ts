// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, downloadAttachment, JSON_HEADERS } from './client'
import type { PutApiV1RepositoriesByNameVexStatementsData, VexStatement as GeneratedVexStatement } from './generated/types.gen'

// VEX decisions are admin-only and keyed by repository name. A decision is the
// (artifact_id, vuln_id) pair's exploitability status; the VEX documents are
// built server-side from the latest scan plus these decisions.

const repoBase = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}`

export type VexStatus = NonNullable<GeneratedVexStatement['status']>
export type VexJustification = NonNullable<PutApiV1RepositoriesByNameVexStatementsData['body']['justification']>

/** The statuses a decision can take, in the order they are offered. */
export const VEX_STATUSES: readonly VexStatus[] = ['affected', 'not_affected', 'fixed', 'under_investigation']

/** OpenVEX justifications; required for `not_affected`, rejected for any other status. */
export const VEX_JUSTIFICATIONS: readonly VexJustification[] = [
  'component_not_present',
  'vulnerable_code_not_present',
  'vulnerable_code_not_in_execute_path',
  'vulnerable_code_cannot_be_controlled_by_adversary',
  'inline_mitigations_already_exist',
]

/** The server rejects longer impact/action statements. */
export const VEX_STATEMENT_MAX_LENGTH = 4096

/** A recorded decision as the list endpoint returns it. */
export type VexRecord = Required<Pick<GeneratedVexStatement, 'id' | 'artifact_id' | 'vuln_id' | 'status'>> &
  Omit<GeneratedVexStatement, 'id' | 'artifact_id' | 'vuln_id' | 'status'>

/** What PUT records for one (artifact_id, vuln_id). */
export type VexDecision = PutApiV1RepositoriesByNameVexStatementsData['body']

/** `artifactId` limits the list to one artifact. */
export async function listVexStatements(repo: string, artifactId?: string): Promise<VexRecord[]> {
  const query = artifactId ? `?${new URLSearchParams({ artifact_id: artifactId })}` : ''
  const res = await request<{ statements?: VexRecord[] }>(`${repoBase(repo)}/vex/statements${query}`)
  return res.statements ?? []
}

/** Records (or replaces) the decision for `decision.artifact_id` + `decision.vuln_id`. */
export function putVexStatement(repo: string, decision: VexDecision): Promise<VexRecord> {
  return request<VexRecord>(`${repoBase(repo)}/vex/statements`, {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify(decision),
  })
}

/** The vulnerability falls back to its derived status. */
export function deleteVexStatement(repo: string, id: string): Promise<void> {
  return request<void>(`${repoBase(repo)}/vex/statements/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export type VexDocumentFormat = 'openvex' | 'cyclonedx'

export const VEX_DOCUMENT_FORMATS: readonly VexDocumentFormat[] = ['openvex', 'cyclonedx']

interface VexDocumentOptions {
  /** Restricts the document to one artifact; otherwise every scanned artifact of the repository is included. */
  artifactId?: string
  /** CycloneDX only: 1.4, 1.5 or 1.6 (server default 1.5). */
  specVersion?: string
}

export function vexDocumentUrl(repo: string, format: VexDocumentFormat, opts: VexDocumentOptions = {}): string {
  const q = new URLSearchParams({ format })
  if (opts.specVersion) q.set('spec_version', opts.specVersion)
  if (opts.artifactId) q.set('artifact_id', opts.artifactId)
  return `${repoBase(repo)}/vex?${q}`
}

/**
 * Downloads a VEX document through the shared authenticated download path: a
 * refusal (403, 400) surfaces as an ApiError, then the browser saves the file.
 */
export function downloadVexDocument(repo: string, format: VexDocumentFormat, opts: VexDocumentOptions = {}): Promise<void> {
  return downloadAttachment(vexDocumentUrl(repo, format, opts), `vex.${format}.json`)
}
