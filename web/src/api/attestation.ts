// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { JSON_HEADERS, request } from './client'
import type { AttestationAttachSettings, AttestationKey } from './generated/types.gen'

export type { AttestationKey }

/**
 * The key that signs SBOM and VEX attestations. Rejects with 501 `not_implemented` while attestations are
 * off, and 409 `secret_key_required` when the server has no `OPENARTIFACT_SECRET_KEY` to protect the key.
 */
export function getAttestationKey(): Promise<AttestationKey> {
  return request<AttestationKey>('/api/v1/admin/attestation')
}

const attachPath = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}/attestation-attach`

/** Whether a hosted docker repository attaches signed attestations to its images as OCI referrers. */
export async function getAttestationAttach(repo: string): Promise<boolean> {
  const res = await request<AttestationAttachSettings>(attachPath(repo))
  return res.enabled
}

export async function setAttestationAttach(repo: string, enabled: boolean): Promise<boolean> {
  const res = await request<AttestationAttachSettings>(attachPath(repo), {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify({ enabled }),
  })
  return res.enabled
}
