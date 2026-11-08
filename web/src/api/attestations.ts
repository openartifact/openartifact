// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { JSON_HEADERS, request } from './client'

export type AttestationKind = 'signature' | 'attestation' | 'sbom'

/** One signature, attestation or SBOM attached to an image (an OCI referrer of its manifest). */
export interface ImageAttestation {
  digest: string
  subject: string
  image: string
  kind: AttestationKind
  predicate_type?: string
  signer_identity?: string
  issuer?: string
  verified: boolean
  verify_error?: string
  created_at: string
}

/** A Fulcio certificate identity a keyless signature must match: `subject` or `subject_regexp`, never both. */
export interface FulcioIdentity {
  issuer: string
  subject?: string
  subject_regexp?: string
}

export interface FulcioTrust {
  roots: string[]
  identities: FulcioIdentity[]
}

/** What a docker repository trusts to verify attachments; `fulcio` is absent while unset. */
export interface AttestationTrust {
  keys: string[]
  fulcio?: FulcioTrust
  require_verified_provenance: boolean
}

/** A decoded in-toto statement. */
export interface AttestationStatement {
  _type?: string
  subject?: { name?: string; digest?: Record<string, string> }[]
  predicateType?: string
  predicate?: unknown
}

const base = (repo: string): string => `/api/v1/repositories/${encodeURIComponent(repo)}`

function query(params: Record<string, string | undefined>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v)
  const s = q.toString()
  return s ? `?${s}` : ''
}

/** The attachments of the image manifest `subject` (`sha256:<hex>`). */
export async function listAttestations(repo: string, subject: string, image?: string): Promise<ImageAttestation[]> {
  const res = await request<{ attestations: ImageAttestation[] | null }>(`${base(repo)}/attestations${query({ subject, image })}`)
  return res.attestations ?? []
}

/** The decoded in-toto statement of one attachment; rejects with 404 when it cannot be read. */
export function getAttestationStatement(repo: string, digest: string, image?: string): Promise<AttestationStatement> {
  return request<AttestationStatement>(`${base(repo)}/attestations/${encodeURIComponent(digest)}${query({ image })}`)
}

/** Verifies the attachments again against the current trust (one manifest, or the whole repository); returns how many were checked. */
export async function reverifyAttestations(repo: string, subject?: string, image?: string): Promise<number> {
  const res = await request<{ reverified: number }>(`${base(repo)}/attestations/reverify${query({ subject, image })}`, { method: 'POST' })
  return res.reverified
}

export function getAttestationTrust(repo: string): Promise<AttestationTrust> {
  return request<AttestationTrust>(`${base(repo)}/attestation-trust`)
}

export function setAttestationTrust(repo: string, cfg: AttestationTrust): Promise<AttestationTrust> {
  return request<AttestationTrust>(`${base(repo)}/attestation-trust`, { method: 'PUT', headers: JSON_HEADERS, body: JSON.stringify(cfg) })
}
