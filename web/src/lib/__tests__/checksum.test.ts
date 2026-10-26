// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { checksumAlgorithm, effectiveMode, looksLikeChecksum, normalizeChecksum } from '../checksum'

const md5 = 'd41d8cd98f00b204e9800998ecf8427e'
const sha1 = 'da39a3ee5e6b4b0d3255bfef95601890afd80709'
const sha256 = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'

describe('checksum detection', () => {
  it('maps full-length digests to their algorithm', () => {
    expect(checksumAlgorithm(md5)).toBe('md5')
    expect(checksumAlgorithm(sha1)).toBe('sha1')
    expect(checksumAlgorithm(sha256)).toBe('sha256')
  })

  it('accepts upper case, surrounding space and an algorithm prefix', () => {
    expect(checksumAlgorithm(`  ${sha256.toUpperCase()} `)).toBe('sha256')
    expect(normalizeChecksum(`sha256:${sha256.toUpperCase()}`)).toBe(sha256)
  })

  it('rejects prefixes, wrong lengths and non-hex text', () => {
    expect(looksLikeChecksum(sha256.slice(0, 12))).toBe(false)
    expect(looksLikeChecksum(sha256 + '0')).toBe(false)
    expect(looksLikeChecksum('z'.repeat(64))).toBe(false)
    expect(looksLikeChecksum('lodash')).toBe(false)
    expect(looksLikeChecksum('')).toBe(false)
  })

  it('lets an explicit mode win over detection', () => {
    expect(effectiveMode('', sha256)).toBe('checksum')
    expect(effectiveMode('', 'lodash')).toBe('name')
    expect(effectiveMode('name', sha256)).toBe('name')
    expect(effectiveMode('checksum', 'lodash')).toBe('checksum')
  })
})
