// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { safeHttpUrl } from '../artifactLinks'

describe('safeHttpUrl', () => {
  it('keeps http and https URLs unchanged', () => {
    expect(safeHttpUrl('https://example.org/a?b=1')).toBe('https://example.org/a?b=1')
    expect(safeHttpUrl('http://example.org')).toBe('http://example.org')
  })
  it('drops other schemes, relative values and empty input', () => {
    expect(safeHttpUrl('javascript:alert(1)')).toBeUndefined()
    expect(safeHttpUrl('/relative')).toBeUndefined()
    expect(safeHttpUrl('')).toBeUndefined()
    expect(safeHttpUrl(undefined)).toBeUndefined()
  })
})
