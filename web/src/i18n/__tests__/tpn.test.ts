// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { tpn } from '..'

describe('tpn', () => {
  it('uses the singular entry for exactly one', () => {
    expect(tpn('search.versionCount', 1)).toBe('1 version')
  })

  it('uses the other entry and fills the count otherwise', () => {
    expect(tpn('search.versionCount', 0)).toBe('0 versions')
    expect(tpn('search.versionCount', 12)).toBe('12 versions')
  })

  it('fills extra placeholders next to the count', () => {
    expect(tpn('shell.palette.count', 3)).toContain('3')
  })
})
