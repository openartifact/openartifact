// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { ALL_FORMATS } from '../formats'
import { pullCommand, repoBaseUrl } from '../installCommands'

describe('installCommands', () => {
  it('produces a base URL and a command for every format', () => {
    for (const f of ALL_FORMATS) {
      expect(repoBaseUrl(f, 'https://oa.example', 'r1')).toContain('oa.example')
      expect(pullCommand(f, 'https://oa.example', 'r1', 'pkg', '1.0.0').length).toBeGreaterThan(0)
    }
  })

  it('uses the native route shape for npm and maven', () => {
    expect(repoBaseUrl('npm', 'https://oa.example', 'r1')).toBe('https://oa.example/repos/r1')
    expect(repoBaseUrl('maven', 'https://oa.example', 'r1')).toBe('https://oa.example/repos/maven/r1')
  })
})
