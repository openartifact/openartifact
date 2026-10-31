// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, jest, beforeEach, afterEach } from 'bun:test'
import { effectScope, nextTick, ref } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { PREVIEW_DEBOUNCE_MS, useCleanupLivePreview } from '../useCleanupLivePreview'
import { emptyCleanupForm } from '../../lib/cleanupPolicy'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const preview = (rows: number) => ({ repository: 'r', format: 'raw', dry_run: true, rows, bytes: rows * 10, items: [], truncated: false })

async function advance(ms: number): Promise<void> {
  jest.advanceTimersByTime(ms)
  await flushPromises()
}

function setup(active = true) {
  const form = ref(emptyCleanupForm())
  const open = ref(active)
  const scope = effectScope()
  const p = scope.run(() => useCleanupLivePreview(ref('my repo'), form, open))!
  return { form, open, p, scope }
}

beforeEach(() => jest.useFakeTimers())
afterEach(() => {
  jest.useRealTimers()
  jest.restoreAllMocks()
})

describe('useCleanupLivePreview', () => {
  it('waits for the edits to settle, then requests once with the rule', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async () => json(preview(3))) as never)
    const { form, p, scope } = setup()

    form.value.keep_count = 2
    await nextTick()
    await advance(PREVIEW_DEBOUNCE_MS - 1)
    form.value.keep_count = 3
    await nextTick()
    await advance(PREVIEW_DEBOUNCE_MS - 1)
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(p.status.value).toBe('loading')

    await advance(1)
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    const [url, init] = fetchSpy.mock.calls[0]!
    expect(String(url)).toBe('/api/v1/repositories/my%20repo/cleanup-policies/dry-run')
    expect((init as RequestInit).method).toBe('POST')
    expect(JSON.parse(String((init as RequestInit).body))).toEqual({ keep_last_n: 3 })
    expect(p.status.value).toBe('ready')
    expect(p.result.value?.rows).toBe(3)
    scope.stop()
  })

  it('aborts the request in flight when the rule changes and ignores its late answer', async () => {
    const signals: AbortSignal[] = []
    const resolvers: Array<(r: Response) => void> = []
    spyOn(globalThis, 'fetch').mockImplementation(((_u: unknown, init?: RequestInit) => {
      signals.push(init!.signal!)
      return new Promise<Response>((resolve) => resolvers.push(resolve))
    }) as never)
    const { form, p, scope } = setup()

    form.value.keep_count = 1
    await nextTick()
    await advance(PREVIEW_DEBOUNCE_MS)
    expect(signals).toHaveLength(1)
    expect(signals[0]!.aborted).toBe(false)

    form.value.keep_count = 2
    await nextTick()
    expect(signals[0]!.aborted).toBe(true)
    await advance(PREVIEW_DEBOUNCE_MS)
    expect(signals).toHaveLength(2)

    resolvers[1]!(json(preview(7)))
    await flushPromises()
    resolvers[0]!(json(preview(99)))
    await flushPromises()
    expect(p.result.value?.rows).toBe(7)
    scope.stop()
  })

  it('does not request for a rule without a usable value, nor for a name edit', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async () => json(preview(1))) as never)
    const { form, p, scope } = setup()
    await advance(PREVIEW_DEBOUNCE_MS)
    expect(fetchSpy).toHaveBeenCalledTimes(1)

    form.value.name = 'renamed'
    form.value.enabled = false
    await nextTick()
    await advance(PREVIEW_DEBOUNCE_MS)
    expect(fetchSpy).toHaveBeenCalledTimes(1)

    form.value.keep_count = undefined
    await nextTick()
    await advance(PREVIEW_DEBOUNCE_MS)
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    expect(p.status.value).toBe('idle')
    expect(p.result.value).toBeNull()
    scope.stop()
  })

  it('cancels a pending request when the editor closes', async () => {
    const fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async () => json(preview(1))) as never)
    const { open, p, scope } = setup()

    open.value = false
    await nextTick()
    await advance(PREVIEW_DEBOUNCE_MS * 2)
    expect(fetchSpy).not.toHaveBeenCalled()
    expect(p.status.value).toBe('idle')
    scope.stop()
  })

  it('reports the server error and recovers on the next edit', async () => {
    let fail = true
    spyOn(globalThis, 'fetch').mockImplementation((async () =>
      fail ? json({ error: 'name_pattern is not a valid regular expression', code: 'validation_failed' }, 400) : json(preview(2))) as never)
    const { form, p, scope } = setup()

    await advance(PREVIEW_DEBOUNCE_MS)
    expect(p.status.value).toBe('error')
    expect(p.error.value).toContain('name_pattern')
    expect(p.result.value).toBeNull()

    fail = false
    form.value.keep_count = 4
    await nextTick()
    await advance(PREVIEW_DEBOUNCE_MS)
    expect(p.status.value).toBe('ready')
    expect(p.error.value).toBe('')
    scope.stop()
  })
})
