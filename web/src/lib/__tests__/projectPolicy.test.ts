// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { policyAllowsPublic, policyAllowsType, policyFormats, policyPrefix, type RepoPolicy } from '../projectPolicy'
import type { FormatId } from '../formats'

const formats = ['npm', 'maven', 'docker'] as FormatId[]
const acme: RepoPolicy = {
  allowed_formats: ['npm'],
  allowed_repo_types: ['hosted'],
  repo_name_prefix: 'acme-',
  allow_public_repos: false,
}

describe('project repository policy', () => {
  it('offers everything when no policy applies', () => {
    expect(policyFormats(null, formats)).toEqual(formats)
    expect(policyAllowsType(null, 'proxy')).toBe(true)
    expect(policyPrefix(null)).toBe('')
    expect(policyAllowsPublic(null)).toBe(true)
  })

  it('narrows formats, types, name and visibility', () => {
    expect(policyFormats(acme, formats)).toEqual(['npm'])
    expect(policyAllowsType(acme, 'hosted')).toBe(true)
    expect(policyAllowsType(acme, 'group')).toBe(false)
    expect(policyPrefix(acme)).toBe('acme-')
    expect(policyAllowsPublic(acme)).toBe(false)
  })

  it('treats an empty or missing allow-list as unrestricted', () => {
    expect(policyFormats({ ...acme, allowed_formats: [] }, formats)).toEqual(formats)
    expect(policyFormats({ ...acme, allowed_formats: null }, formats)).toEqual(formats)
    expect(policyAllowsType({ ...acme, allowed_repo_types: [] }, 'proxy')).toBe(true)
    expect(policyAllowsPublic({ ...acme, allow_public_repos: true })).toBe(true)
  })
})
