// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { tpn } from '../en'

describe('tpn', () => {
  it('uses the singular entry for exactly one', () => {
    expect(tpn('search.versionCount', 1)).toBe('1 version')
  })

  it('uses the plain entry and fills the count otherwise', () => {
    expect(tpn('search.versionCount', 0)).toBe('0 versions')
    expect(tpn('search.versionCount', 12)).toBe('12 versions')
  })

  it('falls back to the plain entry when there is no singular one', () => {
    expect(tpn('quota.percentUsed', 1)).toBe('1% used')
  })
})
