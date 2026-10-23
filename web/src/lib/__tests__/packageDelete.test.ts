// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'vitest'
import { supportsPackageDelete } from '../formats'

describe('supportsPackageDelete', () => {
  it('allows every hosted package format', () => {
    for (const f of ['deb', 'rpm', 'npm', 'maven', 'pypi', 'docker', 'raw', 'go', 'cargo', 'conan']) {
      expect(supportsPackageDelete(f)).toBe(true)
    }
    for (const f of ['', 'unknown']) {
      expect(supportsPackageDelete(f)).toBe(false)
    }
    expect(supportsPackageDelete(undefined)).toBe(false)
  })
})
