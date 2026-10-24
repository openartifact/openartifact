// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import ConnectClientPanel from '../ConnectClientPanel.vue'

const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

async function mountPanel(info: Record<string, unknown>) {
  spyOn(globalThis, 'fetch').mockImplementation((async () => json(info)) as never)
  const w = mount(ConnectClientPanel, { props: { format: 'npm', repo: 'libs' }, global: { plugins: [createPinia()] } })
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
})
