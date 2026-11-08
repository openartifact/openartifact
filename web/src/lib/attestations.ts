// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AttestationTrust, FulcioIdentity } from '../api/attestations'

export type PredicateLabel = 'provenance' | 'signature' | 'sbom' | 'other'

const SLSA = /^https:\/\/slsa\.dev\/provenance\/v(0\.2|1)$/

/** Which short label a predicate type URI gets; `other` shows the raw URI. */
export function predicateLabel(uri: string | undefined): PredicateLabel {
  if (!uri) return 'other'
  if (SLSA.test(uri)) return 'provenance'
  if (uri === 'https://sigstore.dev/cosign/sign/v1') return 'signature'
  if (/^https:\/\/spdx\.dev\/Document/i.test(uri) || /^https:\/\/cyclonedx\.org\/(bom|schema)/i.test(uri)) return 'sbom'
  return 'other'
}

/** An image manifest digest as the attestations endpoint wants it (`sha256:<hex>`), or null when it is not a sha256 one. */
export function subjectDigest(sha: string | undefined): string | null {
  const hex = (sha ?? '').replace(/^sha256:/, '')
  return /^[0-9a-f]{64}$/.test(hex) ? `sha256:${hex}` : null
}

export type IdentityMatch = 'subject' | 'regexp'

export interface IdentityForm {
  issuer: string
  match: IdentityMatch
  value: string
}

/** The trust editor's state: the API shape with an identity's subject/regexp folded into one field. */
export interface TrustForm {
  keys: string[]
  roots: string[]
  identities: IdentityForm[]
  requireProvenance: boolean
}

export function trustToForm(cfg: AttestationTrust): TrustForm {
  return {
    keys: [...cfg.keys],
    roots: [...(cfg.fulcio?.roots ?? [])],
    identities: (cfg.fulcio?.identities ?? []).map((i) =>
      i.subject_regexp ? { issuer: i.issuer, match: 'regexp', value: i.subject_regexp } : { issuer: i.issuer, match: 'subject', value: i.subject ?? '' },
    ),
    requireProvenance: cfg.require_verified_provenance,
  }
}

/** The PUT body: blank entries are dropped and `fulcio` is sent only when it has roots or identities (the server wants both). */
export function formToTrust(form: TrustForm): AttestationTrust {
  const keys = form.keys.map((k) => k.trim()).filter(Boolean)
  const roots = form.roots.map((r) => r.trim()).filter(Boolean)
  const identities = form.identities
    .filter((i) => i.issuer.trim() !== '' || i.value.trim() !== '')
    .map((i): FulcioIdentity => (i.match === 'regexp' ? { issuer: i.issuer.trim(), subject_regexp: i.value.trim() } : { issuer: i.issuer.trim(), subject: i.value.trim() }))
  const body: AttestationTrust = { keys, require_verified_provenance: form.requireProvenance }
  if (roots.length > 0 || identities.length > 0) body.fulcio = { roots, identities }
  return body
}
