// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, jest, beforeEach, afterEach } from 'bun:test'
import { effectScope, ref } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { useFederationStatus } from '../useFederationStatus'

const INTERVAL = 10_000

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const status = (head: number) => ({
  enabled: true,
  supported: true,
  instance_id: 'i',
  log_id: 'l',
  head_seq: head,
  units: 0,
  tombstones: 0,
  peers: [],
  tokens: [],
})

async function advance(ms: number): Promise<void> {
  jest.advanceTimersByTime(ms)
  await flushPromises()
}

function setVisibility(state: 'visible' | 'hidden'): void {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
  document.dispatchEvent(new Event('visibilitychange'))
}

beforeEach(() => jest.useFakeTimers())
afterEach(() => {
  setVisibility('visible')
  jest.useRealTimers()
  jest.restoreAllMocks()
})

describe('useFederationStatus', () => {
  it('loads at once and re-reads every interval', async () => {
    let head = 0
    const fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async () => json(status(++head))) as never)
    const scope = effectScope()
    const s = scope.run(() => useFederationStatus(() => 'repo a'))!
    await flushPromises()
    expect(String(fetchSpy.mock.calls[0]![0])).toBe('/api/v1/repositories/repo%20a/federation')
    expect(s.status.value?.head_seq).toBe(1)
    expect(s.loading.value).toBe(false)

    await advance(INTERVAL)
    expect(s.status.value?.head_seq).toBe(2)
    await advance(INTERVAL)
    expect(s.status.value?.head_seq).toBe(3)
    scope.stop()
  })

  it('does not poll while the page is hidden and refreshes when it becomes visible', async () => {
    let calls = 0
    spyOn(globalThis, 'fetch').mockImplementation((async () => json(status(++calls))) as never)
    const scope = effectScope()
    scope.run(() => useFederationStatus(() => 'r'))
    await flushPromises()
    expect(calls).toBe(1)

    setVisibility('hidden')
    await advance(INTERVAL * 3)
    expect(calls).toBe(1)

    setVisibility('visible')
    await flushPromises()
    expect(calls).toBe(2)
    scope.stop()
  })

  it('clears the timer and stops reading when its scope is disposed', async () => {
    let calls = 0
    spyOn(globalThis, 'fetch').mockImplementation((async () => json(status(++calls))) as never)
    const scope = effectScope()
    scope.run(() => useFederationStatus(() => 'r'))
    await flushPromises()
    scope.stop()
    await advance(INTERVAL * 5)
    expect(calls).toBe(1)

    // The visibility listener is gone too.
    setVisibility('hidden')
    setVisibility('visible')
    await flushPromises()
    expect(calls).toBe(1)
  })

  it('keeps the last good status when a poll fails and recovers on the next one', async () => {
    let calls = 0
    spyOn(globalThis, 'fetch').mockImplementation((async () => {
      calls++
      if (calls === 2) return json({ error: { message: 'boom' } }, 500)
      return json(status(calls))
    }) as never)
    const scope = effectScope()
    const s = scope.run(() => useFederationStatus(() => 'r'))!
    await flushPromises()
    await advance(INTERVAL)
    expect(s.status.value?.head_seq).toBe(1)
    expect(s.error.value).not.toBe('')
    await advance(INTERVAL)
    expect(s.status.value?.head_seq).toBe(3)
    expect(s.error.value).toBe('')
    scope.stop()
  })

  it('drops a response for a repository that was replaced meanwhile', async () => {
    const resolvers: Array<(r: Response) => void> = []
    spyOn(globalThis, 'fetch').mockImplementation((() => {
      const { promise, resolve } = Promise.withResolvers<Response>()
      resolvers.push(resolve)
      return promise
    }) as never)
    const name = ref('a')
    const scope = effectScope()
    const s = scope.run(() => useFederationStatus(() => name.value))!
    await flushPromises()
    name.value = 'b'
    await flushPromises()
    expect(resolvers).toHaveLength(2)
    resolvers[1]!(json(status(2)))
    await flushPromises()
    resolvers[0]!(json(status(1)))
    await flushPromises()
    expect(s.status.value?.head_seq).toBe(2)
    scope.stop()
  })

  it('adopts a mutation result and restarts the poll period', async () => {
    let calls = 0
    spyOn(globalThis, 'fetch').mockImplementation((async () => json(status(++calls))) as never)
    const scope = effectScope()
    const s = scope.run(() => useFederationStatus(() => 'r'))!
    await flushPromises()
    await advance(INTERVAL - 1)
    s.apply(status(99))
    expect(s.status.value?.head_seq).toBe(99)
    await advance(INTERVAL - 1)
    expect(calls).toBe(1)
    await advance(1)
    expect(calls).toBe(2)
    scope.stop()
  })
})
