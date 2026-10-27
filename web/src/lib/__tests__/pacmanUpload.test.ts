// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { isPacmanFile, pacmanArchFor, pacmanUploadOrder, pacmanUploadUrl } from '../pacmanUpload'

describe('pacmanUpload', () => {
  it('recognizes packages and signatures', () => {
    expect(isPacmanFile('tool-1.0-1-x86_64.pkg.tar.zst')).toBe(true)
    expect(isPacmanFile('tool-1.0-1-any.pkg.tar.xz.sig')).toBe(true)
    expect(isPacmanFile('tool-1.0.tar.gz')).toBe(false)
  })

  it('takes the architecture from the file name, any from the chosen directory', () => {
    expect(pacmanArchFor('tool-1.0-1-aarch64.pkg.tar.zst', 'x86_64')).toBe('aarch64')
    expect(pacmanArchFor('tool-1.0-1-any.pkg.tar.zst', 'x86_64_v3')).toBe('x86_64_v3')
    expect(pacmanArchFor('weird.txt', 'x86_64')).toBe('x86_64')
  })

  it('builds the upload route', () => {
    expect(pacmanUploadUrl('my repo', 'tool-1.0-1-x86_64.pkg.tar.zst.sig', 'x86_64')).toBe(
      '/repos/my%20repo/os/x86_64/tool-1.0-1-x86_64.pkg.tar.zst.sig',
    )
  })

  it('uploads packages before signatures', () => {
    const files = ['a.pkg.tar.zst.sig', 'a.pkg.tar.zst', 'b.pkg.tar.xz'].map((n) => new File([''], n))
    expect(pacmanUploadOrder(files).map((f) => f.name)).toEqual(['a.pkg.tar.zst', 'b.pkg.tar.xz', 'a.pkg.tar.zst.sig'])
  })
})
