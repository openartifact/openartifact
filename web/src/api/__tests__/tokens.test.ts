// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { createMyToken, createTokenFor } from '../tokens'

function created(): Response {
  return new Response(JSON.stringify({ token: 't', id: '1', name: 'cli', kind: 'access' }), {
    status: 201,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('token creation', () => {
  it('self-service creation sends the kind the backend requires', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockResolvedValue(created())
    await createMyToken({ name: 'cli', kind: 'access' })
    const init = fetchSpy.mock.calls[0]![1] as RequestInit
    expect(JSON.parse(init.body as string)).toEqual({ name: 'cli', kind: 'access' })
  })

  it('admin creation defaults the kind to access', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockResolvedValue(created())
    await createTokenFor('alice', { name: 'ci' })
    const init = fetchSpy.mock.calls[0]![1] as RequestInit
    expect(JSON.parse(init.body as string)).toMatchObject({ name: 'ci', kind: 'access', principal: 'alice' })
  })
})
