// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { formToTrust, predicateLabel, subjectDigest, trustToForm } from '../attestations'

const HEX = 'a'.repeat(64)

describe('predicateLabel', () => {
  it('maps the known predicate types', () => {
    expect(predicateLabel('https://slsa.dev/provenance/v0.2')).toBe('provenance')
    expect(predicateLabel('https://slsa.dev/provenance/v1')).toBe('provenance')
    expect(predicateLabel('https://sigstore.dev/cosign/sign/v1')).toBe('signature')
    expect(predicateLabel('https://spdx.dev/Document')).toBe('sbom')
    expect(predicateLabel('https://cyclonedx.org/bom')).toBe('sbom')
  })
  it('leaves anything else raw', () => {
    expect(predicateLabel('https://example.com/custom/v1')).toBe('other')
    expect(predicateLabel(undefined)).toBe('other')
  })
})

describe('subjectDigest', () => {
  it('accepts sha256 digests with or without the prefix', () => {
    expect(subjectDigest(HEX)).toBe(`sha256:${HEX}`)
    expect(subjectDigest(`sha256:${HEX}`)).toBe(`sha256:${HEX}`)
  })
  it('rejects anything else', () => {
    expect(subjectDigest('abc')).toBeNull()
    expect(subjectDigest(undefined)).toBeNull()
  })
})

describe('trust form', () => {
  it('round-trips a full configuration', () => {
    const cfg = {
      keys: ['KEY'],
      fulcio: { roots: ['ROOT'], identities: [{ issuer: 'https://i', subject: 'me@x' }, { issuer: 'https://j', subject_regexp: '^a.*$' }] },
      require_verified_provenance: true,
    }
    expect(formToTrust(trustToForm(cfg))).toEqual(cfg)
  })
  it('omits fulcio when unset and drops blank entries', () => {
    const form = trustToForm({ keys: [], require_verified_provenance: false })
    form.keys = ['  K  ', ' ']
    form.roots = ['']
    form.identities = [{ issuer: '', match: 'subject', value: '' }]
    expect(formToTrust(form)).toEqual({ keys: ['K'], require_verified_provenance: false })
  })
  it('keeps half a fulcio block so the server can say what is missing', () => {
    const form = trustToForm({ keys: [], require_verified_provenance: false })
    form.roots = ['ROOT']
    expect(formToTrust(form).fulcio).toEqual({ roots: ['ROOT'], identities: [] })
  })
})
