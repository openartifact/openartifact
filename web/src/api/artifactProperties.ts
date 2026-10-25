// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'

/** Free-form key/value properties attached to one artifact. */
export type ArtifactProperties = Record<string, string>

const propertiesUrl = (repo: string, artifactId: string): string =>
  `/api/v1/repositories/${encodeURIComponent(repo)}/artifacts/${encodeURIComponent(artifactId)}/properties`

export function getArtifactProperties(repo: string, artifactId: string): Promise<ArtifactProperties> {
  return request<ArtifactProperties>(propertiesUrl(repo, artifactId))
}

/** Sets the given keys and leaves the other properties untouched. */
export async function setArtifactProperties(repo: string, artifactId: string, properties: ArtifactProperties): Promise<void> {
  await request<void>(propertiesUrl(repo, artifactId), {
    method: 'PATCH',
    headers: JSON_HEADERS,
    body: JSON.stringify({ properties }),
  })
}

export async function deleteArtifactProperty(repo: string, artifactId: string, key: string): Promise<void> {
  await request<void>(`${propertiesUrl(repo, artifactId)}/${encodeURIComponent(key)}`, { method: 'DELETE' })
}
