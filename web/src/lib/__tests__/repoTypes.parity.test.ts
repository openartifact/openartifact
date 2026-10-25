// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// repoTypes.ts mirrors the format policy of internal/repotype. The Go test
// there writes testdata/web_parity.json; this test checks the frontend against
// it, so a policy change on one side fails until the other follows.

import { describe, it, expect } from 'bun:test'
import { ALL_FORMATS, type FormatId } from '../formats'
import {
  isFederationFormat,
  isFormatAllowed,
  upstreamUrlPlaceholder,
  upstreamUrlRequired,
} from '../repoTypes'
import fixture from '../../../../internal/repotype/testdata/web_parity.json'

interface Row {
  format: FormatId
  proxy: boolean
  group: boolean
  federation: boolean
  default_upstream: string
}

const rows = fixture as Row[]

describe('repoTypes parity with internal/repotype', () => {
  it('covers every frontend format', () => {
    expect(rows.map((r) => r.format).sort()).toEqual([...ALL_FORMATS].sort())
  })

  for (const r of rows) {
    it(r.format, () => {
      expect(isFormatAllowed('proxy', r.format)).toBe(r.proxy)
      expect(isFormatAllowed('group', r.format)).toBe(r.group)
      expect(isFormatAllowed('hosted', r.format)).toBe(true)
      expect(isFederationFormat(r.format)).toBe(r.federation)
      if (r.proxy) expect(upstreamUrlRequired(r.format)).toBe(r.default_upstream === '')
      if (r.default_upstream) expect(upstreamUrlPlaceholder(r.format)).toBe(r.default_upstream)
    })
  }
})
