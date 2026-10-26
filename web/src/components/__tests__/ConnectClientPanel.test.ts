// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import ConnectClientPanel from '../ConnectClientPanel.vue'

const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

async function mountPanel(info: Record<string, unknown>, props: Record<string, unknown> = {}) {
  spyOn(globalThis, 'fetch').mockImplementation((async () => json(info)) as never)
  const w = mount(ConnectClientPanel, { props: { format: 'npm', repo: 'libs', ...props }, global: { plugins: [createPinia()] } })
  await flushPromises()
  return w
}

describe('ConnectClientPanel', () => {
  it('shows the client snippet for the repository and links to the client-setup page', async () => {
    const w = await mountPanel({ version: '1.2.3', docs_url: 'https://docs.example.com/v1.2/' })
    expect(w.text()).toContain('/repos/libs')
    const link = w.get('[data-testid="connect-docs"]')
    expect(link.attributes('href')).toBe('https://docs.example.com/v1.2/formats/client-setup')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener')
  })

  it('keeps the snippet but drops the link when docs links are disabled', async () => {
    const w = await mountPanel({ version: '1.2.3' })
    expect(w.find('[data-testid="connect-docs"]').exists()).toBe(false)
    expect(w.text()).toContain('/repos/libs')
  })

  it('offers the publish command of a hosted repository', async () => {
    const w = await mountPanel({ version: '1.2.3' })
    expect(w.findAll('h3').map((h) => h.text())).toEqual(['Configure', 'Publish'])
    expect(w.text()).toContain('npm publish')
    expect(w.find('[data-testid="connect-read-only"]').exists()).toBe(false)
  })

  it('keeps a read-only repository to its read commands and says why', async () => {
    const w = await mountPanel({ version: '1.2.3' }, { readOnly: true })
    expect(w.findAll('h3').map((h) => h.text())).toEqual(['Configure'])
    expect(w.text()).not.toContain('npm publish')
    expect(w.get('[data-testid="connect-read-only"]').text()).toContain('read-only')
    expect(w.text()).toContain('/repos/libs')
  })

  it('calls the read command of a raw repository a download', async () => {
    const w = await mountPanel({ version: '1.2.3' }, { format: 'raw' })
    expect(w.findAll('h3').map((h) => h.text())).toEqual(['Download', 'Publish'])
  })
})
