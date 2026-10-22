// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { safeNextPath } from '../safeRedirect'

describe('safeNextPath', () => {
  it('keeps same-origin paths', () => {
    expect(safeNextPath('/repos/foo?x=1#y')).toBe('/repos/foo?x=1#y')
    expect(safeNextPath('/')).toBe('/')
  })

  it('rejects off-site and malformed targets', () => {
    for (const v of [
      '//evil.example/x',
      '/\\evil.example',
      'https://evil.example',
      'javascript:alert(1)',
      '/a\\b',
      '/a\nb',
      'evil',
      '',
      undefined,
      null,
      ['//evil.example'],
    ]) {
      expect(safeNextPath(v)).toBe('/')
    }
  })
})
