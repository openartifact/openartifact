// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import OsvMirrorCard from '../security/OsvMirrorCard.vue'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const ATTRIBUTION = { source: 'OSV', license: 'CC-BY-4.0', url: 'https://osv.dev' }
const eco = (over: Record<string, unknown> = {}) => ({
  ecosystem: 'npm', populated: true, advisories: 1234, high_water: null, last_checked_at: null,
  last_imported_at: '2026-01-01T00:00:00Z', last_import_seconds: 10, last_rematch_seconds: 3,
  last_error: null, rematch_pending: false, ...over,
})

let spy: ReturnType<typeof spyOn> | undefined

async function mountCard(body: unknown, status = 200) {
  spy = spyOn(globalThis, 'fetch').mockResolvedValue(json(body, status))
  const w = mount(OsvMirrorCard)
  await flushPromises()
  return { w, spy: spy! }
}

afterEach(() => {
  spy?.mockRestore()
})

describe('OsvMirrorCard', () => {
  it('says the online API is used when the mirror is disabled and keeps the attribution', async () => {
    const { w } = await mountCard({ enabled: false, source_url: '', interval_seconds: 0, ecosystems: [], attribution: ATTRIBUTION })
    expect(w.get('[data-testid=osv-mirror-state]').text()).toBe('Disabled')
    expect(w.get('[data-testid=osv-mirror-disabled]').text()).toContain('online')
    expect(w.find('[data-testid=osv-mirror-table]').exists()).toBe(false)
    expect(w.get('[data-testid=osv-attribution] a').attributes('href')).toBe('https://osv.dev')
  })

  it('lists ecosystems and flags an import error', async () => {
    const { w, spy } = await mountCard({
      enabled: true, source_url: 'https://osv-vulnerabilities.storage.googleapis.com', interval_seconds: 86400,
      ecosystems: [eco(), eco({ ecosystem: 'PyPI', last_error: 'download failed' }), eco({ ecosystem: 'Go', populated: false })],
      attribution: ATTRIBUTION,
    })
    expect(spy.mock.calls[0]![0]).toBe('/api/v1/vuln/osv-mirror')
    expect(w.get('[data-testid=osv-mirror-state]').text()).toBe('Enabled')
    expect(w.get('[data-testid=osv-eco-npm]').text()).toContain('1,234')
    expect(w.get('[data-testid=osv-eco-PyPI] [data-testid=osv-eco-error]').attributes('title')).toBe('download failed')
    expect(w.get('[data-testid=osv-eco-Go]').text()).toContain('Waiting')
  })

  it('shows a load failure', async () => {
    const { w } = await mountCard({ error: 'boom' }, 500)
    expect(w.find('[data-testid=osv-mirror-error]').exists()).toBe(true)
  })
})
