// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { ApiError } from '../../api/client'
import { absoluteUrl, attestationProblem, verifySnippet } from '../attestation'

describe('attestationProblem', () => {
  it('reads 501 as attestations being off', () => {
    expect(attestationProblem(new ApiError('nope', 501, 'not_implemented'))).toBe('off')
  })
  it('reads 409 secret_key_required as the missing secret key', () => {
    expect(attestationProblem(new ApiError('x', 409, 'secret_key_required'))).toBe('secret-required')
  })
  it('treats any other failure as an error', () => {
    expect(attestationProblem(new ApiError('x', 409, 'other'))).toBe('error')
    expect(attestationProblem(new ApiError('x', 500))).toBe('error')
    expect(attestationProblem(new Error('network'))).toBe('error')
  })
})

describe('verifySnippet', () => {
  it('fetches the key from the given URL and verifies with it', () => {
    const s = verifySnippet('https://oa.test/key.pem')
    expect(s.split('\n')[0]).toBe('curl -sS https://oa.test/key.pem -o attestation-key.pem')
    expect(s).toContain('cosign verify-blob-attestation')
    expect(s).toContain('--key attestation-key.pem')
  })
})

describe('absoluteUrl', () => {
  it('prefixes a path with the origin and leaves absolute URLs alone', () => {
    expect(absoluteUrl('/api/key', 'https://oa.test')).toBe('https://oa.test/api/key')
    expect(absoluteUrl('http://other/k', 'https://oa.test')).toBe('http://other/k')
  })
})
