// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest'
import { supportsPackageDelete } from '../formats'

describe('supportsPackageDelete', () => {
  it('allows deb and rpm only', () => {
    expect(supportsPackageDelete('deb')).toBe(true)
    expect(supportsPackageDelete('rpm')).toBe(true)
    for (const f of ['npm', 'maven', 'pypi', 'docker', 'raw', 'go', '']) {
      expect(supportsPackageDelete(f)).toBe(false)
    }
    expect(supportsPackageDelete(undefined)).toBe(false)
  })
})
