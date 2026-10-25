// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request } from './client'

/** One stored SBOM report of a repository. */
export interface SbomItem {
  id: string
  format: string
  spec_version?: string
  component_count?: number
  created_at?: string
}

/** Subset of a CycloneDX JSON document, as streamed by the /download route. */
export interface CycloneDxDocument {
  components?: Array<{ 'bom-ref'?: string; name: string; version?: string }>
  dependencies?: Array<{ ref: string; dependsOn?: string[] }>
  vulnerabilities?: Array<{ affects?: Array<{ ref: string }> }>
}

const sbomUrl = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}/sbom`

export async function listSboms(repo: string): Promise<SbomItem[]> {
  const res = await request<{ reports?: SbomItem[] }>(sbomUrl(repo))
  return res?.reports ?? []
}

export function getSbomDocument(repo: string, id: string): Promise<CycloneDxDocument> {
  return request<CycloneDxDocument>(`${sbomUrl(repo)}/${encodeURIComponent(id)}/download`)
}
