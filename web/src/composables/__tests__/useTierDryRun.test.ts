// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import { useTierDryRun } from '../useTierDryRun'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

afterEach(() => {
  ;(globalThis.fetch as unknown as { mockRestore?: () => void }).mockRestore?.()
})

function deferredFetch() {
  const resolvers: Array<(r: Response) => void> = []
  spyOn(globalThis, 'fetch').mockImplementation(
    () => new Promise<Response>((resolve) => resolvers.push(resolve)),
  )
  return resolvers
}

const flush = () => new Promise((r) => setTimeout(r, 0))

describe('useTierDryRun', () => {
  it('drops a slow response that arrives after a newer run()', async () => {
    const resolvers = deferredFetch()
    const d = useTierDryRun()
    const first = d.run()
    const second = d.run()
    resolvers[1]!(json({ scanned: 2 }))
    await second
    resolvers[0]!(json({ scanned: 1 }))
    await first
    expect((d.result.value as { scanned?: number }).scanned).toBe(2)
    expect(d.loading.value).toBe(false)
  })

  it('drops a response that arrives after reset()', async () => {
    const resolvers = deferredFetch()
    const d = useTierDryRun()
    const pending = d.run()
    d.reset()
    resolvers[0]!(json({ scanned: 1 }))
    await pending
    await flush()
    expect(d.result.value).toBeNull()
    expect(d.loading.value).toBe(false)
    expect(d.error.value).toBe('')
  })

  it('drops a failure that arrives after reset()', async () => {
    const resolvers = deferredFetch()
    const d = useTierDryRun()
    const pending = d.run()
    d.reset()
    resolvers[0]!(json({ error: 'boom' }, 500))
    await pending
    expect(d.error.value).toBe('')
  })
})
