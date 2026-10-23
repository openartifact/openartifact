// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// The server sends `script-src 'self'` (web/embed.go), so index.html must not
// carry inline scripts: the browser would block them and log a CSP error.
describe('index.html', () => {
  const html = readFileSync(resolve(import.meta.dir, '../../index.html'), 'utf8')
  const scripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script[^>]*>/gi)]

  it('has no inline script', () => {
    const inline = scripts.filter(([, attrs]) => !/\bsrc=/i.test(attrs))
    expect(inline).toEqual([])
  })

  it('loads the theme guard before the app bundle', () => {
    const srcs = scripts.map(([, attrs]) => /\bsrc="([^"]+)"/.exec(attrs)?.[1])
    expect(srcs).toEqual(['/theme-init.js', '/src/main.ts'])
  })
})
