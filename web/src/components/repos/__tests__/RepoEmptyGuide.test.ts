// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import RepoEmptyGuide from '../RepoEmptyGuide.vue'
import { FORMAT_IDS } from '../../../lib/formats.generated'
import { clientSetup } from '../../../lib/installCommands'

async function mountGuide(format: string, canUpload = false) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { render: () => null } }] })
  await router.push('/')
  return mount(RepoEmptyGuide, { props: { format, repo: 'my-repo', canUpload }, global: { plugins: [pinia, router] } })
}

describe('RepoEmptyGuide', () => {
  for (const format of FORMAT_IDS) {
    it(`teaches publish and consume for ${format} with this repository's URL`, async () => {
      const w = await mountGuide(format)
      const shown = w.findAll('[data-testid="snippet"]').map((s) => s.text())
      const setups = clientSetup(format, window.location.origin, 'my-repo', 'hosted')
      for (const s of setups) {
        expect(shown).toContain(s.configure)
        if (s.publish) expect(shown).toContain(s.publish)
      }
      expect(w.find('[data-testid="empty-publish"]').exists()).toBe(true)
      expect(w.find('[data-testid="empty-consume"]').exists()).toBe(true)
      expect(shown.join('\n')).toMatch(/my-repo|openartifact/)
    })
  }

  it('shows the npm registry commands and links token creation, not passwords', async () => {
    const w = await mountGuide('npm')
    expect(w.find('[data-testid="empty-publish"]').text()).toContain(`npm publish --registry ${window.location.origin}/repos/my-repo/`)
    expect(w.find('[data-testid="empty-token-link"]').attributes('href')).toBe('/settings/tokens')
    expect(w.find('[data-testid="empty-token-hint"]').text().toLowerCase()).toContain('never your account password')
  })

  it('offers the upload button only when the format can be uploaded in the UI', async () => {
    expect((await mountGuide('npm')).find('[data-testid="empty-upload"]').exists()).toBe(false)
    const w = await mountGuide('rpm', true)
    await w.find('[data-testid="empty-upload"]').trigger('click')
    expect(w.emitted('upload')).toHaveLength(1)
  })
})
