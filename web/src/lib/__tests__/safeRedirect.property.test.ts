// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { safeNextPath } from '../safeRedirect'
import { fc, hostileString } from './propertySetup'

const ORIGIN = 'https://app.test'

// Strings shaped like redirect attacks: slashes, backslashes, schemes, control characters, whitespace.
const attack = fc
  .array(fc.oneof(fc.constantFrom('/', '/', '\\', '//', '/\\', '\t', '\n', '\r', ' ', '\u0000', '\u007f', '\u2028', ':', '@', '.', '%2F', '%5C', 'https:', 'javascript:', 'evil.example', '?', '#'), hostileString), { maxLength: 8 })
  .map((parts) => parts.join(''))
const input = fc.oneof(attack, fc.string({ unit: 'grapheme' }), attack.map((s) => `/${s}`), attack.map((s) => `//${s}`))

describe('safeNextPath (property)', () => {
  it('returns either the input unchanged or the site root', () => {
    fc.assert(fc.property(input, (next) => {
      const out = safeNextPath(next)
      expect(out === next || out === '/').toBe(true)
    }))
  })

  it('only ever returns a path that resolves on the app origin, with no backslash or control character', () => {
    fc.assert(fc.property(input, (next) => {
      const out = safeNextPath(next)
      expect(out.startsWith('/')).toBe(true)
      expect(out.startsWith('//')).toBe(false)
      expect(/[\\\u0000-\u001f\u007f]/.test(out)).toBe(false)
      const url = new URL(out, ORIGIN)
      expect(url.origin).toBe(ORIGIN)
      expect(url.protocol).toBe('https:')
    }))
  })

  it('keeps every same-origin path made of plain segments, query and fragment', () => {
    fc.assert(fc.property(fc.array(fc.stringMatching(/^[A-Za-z0-9._~%-]{1,8}$/), { maxLength: 4 }), fc.option(fc.stringMatching(/^[a-z0-9=&]{1,8}$/), { nil: undefined }), (segs, query) => {
      const path = `/${segs.join('/')}${query ? `?${query}` : ''}`
      fc.pre(!path.startsWith('//'))
      expect(safeNextPath(path)).toBe(path)
    }))
  })

  it('sends anything that is not a string to the site root', () => {
    fc.assert(fc.property(fc.oneof(fc.constant(undefined), fc.constant(null), fc.integer(), fc.boolean(), fc.array(input), fc.object()), (next) => {
      expect(safeNextPath(next)).toBe('/')
    }))
  })
})
