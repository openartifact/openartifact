// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach, type Mock } from 'bun:test'
import { ApiError } from '../client'
import { createVexFeed, deleteVexFeed, listVexFeeds, syncVexFeed, updateVexFeed } from '../vexFeeds'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch>
afterEach(() => spy?.mockRestore())

const lastUrl = (): string => String(spy.mock.calls.at(-1)![0])
const lastInit = (): RequestInit => spy.mock.calls.at(-1)![1] as RequestInit

const BODY = { name: 'acme', url: 'https://acme.example/.well-known/csaf/provider-metadata.json', interval_hours: 24 }

describe('vex feeds api', () => {
  it('lists the feeds', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ feeds: [{ id: 'f1' }] }))
    expect(await listVexFeeds()).toEqual([{ id: 'f1' } as never])
    expect(lastUrl()).toBe('/api/v1/vex/feeds')
    expect(lastInit()?.method ?? 'GET').toBe('GET')

    spy.mockImplementation(async () => json({}))
    expect(await listVexFeeds()).toEqual([])
  })

  it('creates with POST and replaces with PUT, sending the body unchanged', async () => {
    spy = spyOn(globalThis, 'fetch').mockImplementation(async () => json({ id: 'f1' }, 201))
    await createVexFeed(BODY)
    expect(lastUrl()).toBe('/api/v1/vex/feeds')
    expect(lastInit().method).toBe('POST')
    expect(JSON.parse(lastInit().body as string)).toEqual(BODY)

    await updateVexFeed('f/1', BODY)
    expect(lastUrl()).toBe('/api/v1/vex/feeds/f%2F1')
    expect(lastInit().method).toBe('PUT')
    expect(JSON.parse(lastInit().body as string)).toEqual(BODY)
    expect(new Headers(lastInit().headers).get('content-type')).toBe('application/json')
  })

  it('deletes a feed', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))
    await deleteVexFeed('f1')
    expect(lastUrl()).toBe('/api/v1/vex/feeds/f1')
    expect(lastInit().method).toBe('DELETE')
  })

  it('starts a sync and reports one already running as a 409', async () => {
    spy = spyOn(globalThis, 'fetch').mockResolvedValue(json({ status: 'started' }, 202))
    await syncVexFeed('f1')
    expect(lastUrl()).toBe('/api/v1/vex/feeds/f1/sync')
    expect(lastInit().method).toBe('POST')

    spy.mockResolvedValue(json({ error: 'a sync of this feed is already running', code: 'conflict' }, 409))
    const err = await syncVexFeed('f1').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
  })
})
