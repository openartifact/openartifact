// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, spyOn, type Mock } from 'bun:test'
import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import AvailabilityPanel from '../artifact/AvailabilityPanel.vue'
import type { AvailabilityResponse } from '../../api/curation'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let spy: Mock<typeof fetch> | undefined
const urls: string[] = []

function serve(res: AvailabilityResponse | Response): void {
  spy?.mockRestore()
  urls.length = 0
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
    urls.push(String(input))
    return res instanceof Response ? res.clone() : json(res)
  }) as typeof fetch)
}

afterEach(() => spy?.mockRestore())

async function mountPanel(over: { enabled?: boolean; canManage?: boolean } = {}) {
  const w = mount(AvailabilityPanel, {
    props: { repo: 'npm proxy', pkg: '@acme/lib', version: '1.0.0', enabled: true, canManage: false, ...over },
    global: { stubs: { RouterLink: RouterLinkStub } },
  })
  await flushPromises()
  return w
}

const until = (): string => new Date(Date.now() + 5 * 3_600_000 - 1000).toISOString()

describe('AvailabilityPanel', () => {
  it('asks the availability of the shown version and says nothing of an available one', async () => {
    serve({ available: true, reasons: [], min_age: 'off' })
    const w = await mountPanel()
    expect(urls).toEqual(['/api/v1/repositories/npm%20proxy/availability?package=%40acme%2Flib&version=1.0.0'])
    expect(w.find('[data-testid="availability-panel"]').exists()).toBe(false)
  })

  it('lists each hold with its source, detail and a countdown', async () => {
    serve({
      available: false,
      min_age: 'checked',
      published_at: '2026-10-31T08:00:00Z',
      reasons: [
        { source: 'min_age', detail: "version is younger than the repository's minimum age", until: until() },
        { source: 'curation', detail: 'blocked by repository curation: malware', rule_id: 'r1' },
      ],
    })
    const w = await mountPanel()
    const rows = w.findAll('[data-testid="availability-reason"]')
    expect(rows).toHaveLength(2)
    expect(rows[0]!.text()).toContain('Minimum age')
    expect(w.find('[data-testid="availability-until"]').text()).toBe('Available in 5h')
    expect(rows[1]!.text()).toContain('Curation')
    expect(rows[1]!.text()).toContain('malware')
    expect(rows[1]!.text()).not.toContain('blocked by repository curation')
  })

  it('links the curation rules only for those who can manage the repository', async () => {
    const res: AvailabilityResponse = { available: false, min_age: 'off', reasons: [{ source: 'curation', detail: 'blocked by repository curation', rule_id: 'r1' }] }
    serve(res)
    expect((await mountPanel()).find('[data-testid="availability-rule-link"]').exists()).toBe(false)
    serve(res)
    const link = (await mountPanel({ canManage: true })).findComponent(RouterLinkStub)
    expect(link.props('to')).toEqual({ path: '/repos/npm%20proxy', query: { tab: 'settings' } })
  })

  it('notes an age check that could not run, and shows nothing without a verdict', async () => {
    serve({ available: true, reasons: [], min_age: 'unknown' })
    expect((await mountPanel()).find('[data-testid="availability-unknown-age"]').exists()).toBe(true)
    serve(json({ error: 'x' }, 500))
    expect((await mountPanel()).find('section').exists()).toBe(false)
    serve({ available: false, reasons: [], min_age: 'off' })
    await mountPanel({ enabled: false })
    expect(urls).toEqual([])
  })
})
