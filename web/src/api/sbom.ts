// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, downloadAttachment, JSON_HEADERS } from './client'

/** One stored SBOM report; `artifact_id` is the package row (file) it describes. */
export interface SbomItem {
  id: string
  artifact_id: string
  format: string
  spec_version?: string
  component_count?: number
  generated_at?: string
  created_at?: string
}

/** Document formats a report can be generated in. */
export const SBOM_FORMATS = ['cyclonedx', 'spdx', 'spdx-json', 'spdx3-json'] as const
export type SbomFormat = (typeof SBOM_FORMATS)[number]

/** Subset of a CycloneDX JSON document, as streamed by the /download route. */
export interface CycloneDxDocument {
  components?: Array<{ 'bom-ref'?: string; name: string; version?: string }>
  dependencies?: Array<{ ref: string; dependsOn?: string[] }>
  vulnerabilities?: Array<{ affects?: Array<{ ref: string }> }>
}

const sbomUrl = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}/sbom`

const downloadUrl = (repo: string, id: string): string => `${sbomUrl(repo)}/${encodeURIComponent(id)}/download`

export async function listSboms(repo: string): Promise<SbomItem[]> {
  const res = await request<{ reports?: SbomItem[] }>(sbomUrl(repo))
  return res?.reports ?? []
}

export function getSbomDocument(repo: string, id: string): Promise<CycloneDxDocument> {
  return request<CycloneDxDocument>(downloadUrl(repo, id))
}

/** Generates (and stores) the report of one package row. */
export function generateSbom(repo: string, artifactId: string, format: SbomFormat): Promise<void> {
  return request<void>(`${sbomUrl(repo)}/generate`, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify({ artifact_id: artifactId, format }),
  })
}

/** Saves the stored document; the server names the file. */
export function downloadSbom(repo: string, report: Pick<SbomItem, 'id' | 'format'>): Promise<void> {
  return downloadAttachment(downloadUrl(repo, report.id), `sbom-${report.format}.json`)
}

export function deleteSbom(repo: string, id: string): Promise<void> {
  return request<void>(`${sbomUrl(repo)}/${encodeURIComponent(id)}`, { method: 'DELETE' })
}
