// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// installCommands.ts is the TypeScript twin of internal/clientsetup. The Go
// test there writes testdata/web_parity.json; this test replays its inputs
// through the frontend and expects identical output, so a change on one side
// fails until the other follows.

import { describe, it, expect } from 'bun:test'
import { ALL_FORMATS } from '../formats'
import { clientSetup, repoBaseUrl } from '../installCommands'
import fixture from '../../../../internal/clientsetup/testdata/web_parity.json'

interface Case {
  format: string
  origin: string
  repo: string
  base_url: string
  snippets: Array<{ client: string; configure: string; publish?: string }>
}

const cases = fixture as Case[]

describe('installCommands parity with internal/clientsetup', () => {
  it('covers every frontend format', () => {
    expect([...new Set(cases.map((c) => c.format))].sort()).toEqual([...ALL_FORMATS].sort())
  })

  for (const c of cases) {
    it(`${c.format} / ${c.repo}`, () => {
      expect(repoBaseUrl(c.format, c.origin, c.repo)).toBe(c.base_url)
      expect(clientSetup(c.format, c.origin, c.repo)).toEqual(c.snippets)
    })
  }
})
