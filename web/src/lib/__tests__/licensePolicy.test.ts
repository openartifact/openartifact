// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { licensePolicyNameProblem, parseLicenseList, policyAction } from '../licensePolicy'

describe('parseLicenseList', () => {
  it('splits on commas, semicolons, spaces and new lines, trimming and dropping duplicates', () => {
    expect(parseLicenseList('GPL-3.0-only, AGPL-3.0-only\nMIT;  GPL-3.0-only ')).toEqual(['GPL-3.0-only', 'AGPL-3.0-only', 'MIT'])
  })
  it('is empty for blank text', () => {
    expect(parseLicenseList(' \n , ')).toEqual([])
  })
})

describe('licensePolicyNameProblem', () => {
  it('requires a name and caps its length', () => {
    expect(licensePolicyNameProblem('  ')).toBe('required')
    expect(licensePolicyNameProblem('x'.repeat(256))).toBe('tooLong')
    expect(licensePolicyNameProblem('No GPL')).toBeNull()
  })
})

describe('policyAction', () => {
  it('falls back to warn for an empty or unknown action', () => {
    expect(policyAction({ action: null })).toBe('warn')
    expect(policyAction({ action: 'block' })).toBe('block')
  })
})
