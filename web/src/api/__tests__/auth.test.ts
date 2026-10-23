// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { logout, safeEndSessionURL } from '../auth'

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

describe('logout', () => {
  it('resolves to the IdP end-session URL when the server returns one', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockResolvedValue(
      json({ end_session_url: 'https://idp.example/logout?id_token_hint=abc' }),
    )

    expect(await logout()).toBe('https://idp.example/logout?id_token_hint=abc')
    expect(fetchSpy.mock.calls[0]![0]).toBe('/api/v1/auth/logout')
    expect((fetchSpy.mock.calls[0]![1] as RequestInit).method).toBe('POST')
  })

  it('resolves to undefined for a plain local logout (204)', async () => {
    spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))

    expect(await logout()).toBeUndefined()
  })

  it('never hands a non-http(s) URL to the navigation', async () => {
    spyOn(globalThis, 'fetch').mockResolvedValue(json({ end_session_url: 'javascript:alert(1)' }))

    expect(await logout()).toBeUndefined()
  })
})

describe('safeEndSessionURL', () => {
  const cases: [string, string | undefined, string | undefined][] = [
    ['https url', 'https://idp.example/logout?a=b', 'https://idp.example/logout?a=b'],
    ['http url (dev IdP)', 'http://localhost:9000/logout', 'http://localhost:9000/logout'],
    ['javascript scheme', 'javascript:alert(1)', undefined],
    ['data scheme', 'data:text/html,<script>1</script>', undefined],
    ['relative path', '/logout', undefined],
    ['empty', '', undefined],
    ['missing', undefined, undefined],
  ]

  for (const [name, input, want] of cases) {
    it(name, () => {
      expect(safeEndSessionURL(input)).toBe(want)
    })
  }
})
