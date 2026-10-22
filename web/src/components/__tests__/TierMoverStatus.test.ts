// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, jest, beforeEach, afterEach } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import TierMoverStatus from '../TierMoverStatus.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const PASS = (finished: string) => ({
  started_at: '2026-01-01T00:00:00Z',
  finished_at: finished,
  recovered: 0,
  promoted: 1,
  moved: 7,
  skipped: 2,
  failed: 0,
  bytes: 2048,
  scanned: 100,
  aborted: false,
})

const INTERVAL = 1000

/** Advances the fake clock, letting the awaited fetch/parse chain settle after each step. */
async function advance(ms: number): Promise<void> {
  jest.advanceTimersByTime(ms)
  await flushPromises()
}

function mountStatus(props: { pollIntervalMs?: number; maxPolls?: number; maxFailures?: number } = {}) {
  return mount(TierMoverStatus, { props: { pollIntervalMs: INTERVAL, ...props }, global: { plugins: [createPinia()] } })
}

beforeEach(() => jest.useFakeTimers())
afterEach(() => {
  jest.useRealTimers()
  jest.restoreAllMocks()
})

describe('TierMoverStatus', () => {
  it('shows the last pass and pending moves, and disables Run now when the mover is off', async () => {
    spyOn(globalThis, 'fetch').mockImplementation((async () =>
      json({
        enabled: false,
        running: false,
        interval_seconds: 300,
        last_pass: PASS('2026-01-01T00:01:00Z'),
        pending_total: 1,
        pending: [{ sha256: 'c'.repeat(64), size_bytes: 10, from: 'hot', to: 'cold', state: 'copying', reason: 'timeout', attempts: 2, updated_at: '2026-01-01T00:00:30Z' }],
      })) as never)
    const wrapper = mountStatus()
    await flushPromises()

    expect(wrapper.get('[data-testid="mover-last-pass"]').text()).toContain('7')
    expect(wrapper.get('[data-testid="mover-pending"]').text()).toContain('hot → cold')
    expect(wrapper.get('[data-testid="mover-run"]').attributes('disabled')).toBeDefined()
  })

  const status = (running: boolean, finished: string) => ({
    enabled: true,
    running,
    interval_seconds: 300,
    pending_total: 0,
    pending: [],
    last_pass: PASS(finished),
  })

  it('requests a pass, keeps polling while running with growing delay, then stops and notifies', async () => {
    let statusCalls = 0
    const fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async (url: string) => {
      if (String(url).endsWith('/run')) return json({ started: true }, 202)
      statusCalls++
      if (statusCalls === 1) return json(status(false, '2026-01-01T00:01:00Z'))
      if (statusCalls <= 6) return json(status(true, '2026-01-01T00:01:00Z'))
      return json(status(false, '2026-01-01T00:09:00Z'))
    }) as never)

    const wrapper = mountStatus({ maxPolls: 2 })
    await flushPromises()
    await wrapper.get('[data-testid="mover-run"]').trigger('click')
    await flushPromises()

    // Delays back off: 1s, 2s, 4s, ...; a fixed 1s step must not be enough for the second read.
    await advance(INTERVAL)
    expect(statusCalls).toBe(2)
    await advance(INTERVAL)
    expect(statusCalls).toBe(2)
    await advance(INTERVAL)
    expect(statusCalls).toBe(3)
    for (let i = 0; i < 8; i++) await advance(INTERVAL * 8)
    expect(wrapper.emitted('pass-finished')).toHaveLength(1)

    const callsAtFinish = statusCalls
    await advance(INTERVAL * 100)
    expect(statusCalls).toBe(callsAtFinish)
    expect(fetchSpy.mock.calls.some(([u, i]) => String(u).endsWith('/run') && i?.method === 'POST')).toBe(true)
  })

  it('stops after maxPolls idle reads where no pass ran', async () => {
    let statusCalls = 0
    spyOn(globalThis, 'fetch').mockImplementation((async (url: string) => {
      if (String(url).endsWith('/run')) return json({ started: true }, 202)
      statusCalls++
      return json(status(false, '2026-01-01T00:01:00Z'))
    }) as never)

    const wrapper = mountStatus({ maxPolls: 3 })
    await flushPromises()
    await wrapper.get('[data-testid="mover-run"]').trigger('click')
    await flushPromises()
    for (let i = 0; i < 10; i++) await advance(INTERVAL * 8)
    expect(statusCalls).toBe(4)
    expect(wrapper.emitted('pass-finished')).toBeUndefined()
  })

  it('stops after consecutive failures, shows an error and recovers on Refresh', async () => {
    let statusCalls = 0
    let healthy = false
    spyOn(globalThis, 'fetch').mockImplementation((async (url: string) => {
      if (String(url).endsWith('/run')) return json({ started: true }, 202)
      statusCalls++
      if (statusCalls === 1) return json(status(true, '2026-01-01T00:01:00Z'))
      if (!healthy) return json({ error: 'boom' }, 500)
      return json(status(false, '2026-01-01T00:09:00Z'))
    }) as never)

    const wrapper = mountStatus({ maxFailures: 2 })
    await flushPromises()
    for (let i = 0; i < 6; i++) await advance(INTERVAL * 8)
    expect(statusCalls).toBe(3) // load + 2 failed polls, then silence
    expect(wrapper.find('[data-testid="mover-poll-error"]').exists()).toBe(true)
    // Last known status stays visible.
    expect(wrapper.find('[data-testid="mover-last-pass"]').exists()).toBe(true)

    healthy = true
    await wrapper.get('[data-testid="mover-poll-error"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="mover-poll-error"]').exists()).toBe(false)
  })

  it('a successful poll resets the failure count', async () => {
    let statusCalls = 0
    spyOn(globalThis, 'fetch').mockImplementation((async () => {
      statusCalls++
      if (statusCalls === 3 || statusCalls === 5) return json({ error: 'boom' }, 500)
      if (statusCalls === 7) return json(status(false, '2026-01-01T00:09:00Z'))
      return json(status(true, '2026-01-01T00:01:00Z'))
    }) as never)

    const wrapper = mountStatus({ maxFailures: 2 })
    await flushPromises()
    for (let i = 0; i < 8; i++) await advance(INTERVAL * 8)
    expect(wrapper.emitted('pass-finished')).toHaveLength(1)
  })

  it('stops polling when unmounted and ignores a response in flight', async () => {
    let statusCalls = 0
    let release: (() => void) | undefined
    spyOn(globalThis, 'fetch').mockImplementation((async () => {
      statusCalls++
      if (statusCalls === 2) await new Promise<void>((r) => (release = r))
      return json(status(true, '2026-01-01T00:01:00Z'))
    }) as never)

    const wrapper = mountStatus()
    await flushPromises()
    await advance(INTERVAL * 2)
    expect(statusCalls).toBe(2) // second read is in flight
    wrapper.unmount()
    release?.()
    await flushPromises()
    await advance(INTERVAL * 100)
    expect(statusCalls).toBe(2)
  })
})
