// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import SettingsHubPage from '../SettingsHubPage.vue'
import { SETTINGS_SECTIONS } from '../../../lib/settingsSections'
import { useSessionStore, type User } from '../../../stores/session'

async function mountHub(isAdmin: boolean, path = '/settings') {
  setActivePinia(createPinia())
  useSessionStore().user = { id: 'u', username: 'dev', is_admin: isAdmin } as User
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/settings', component: SettingsHubPage },
      { path: '/settings/:section', component: { render: () => null } },
    ],
  })
  await router.push(path)
  const wrapper = mount(SettingsHubPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return { router, wrapper }
}

describe('SettingsHubPage', () => {
  beforeEach(() => document.body.replaceChildren())

  it('lists the seven groups with every section for an administrator', async () => {
    const { wrapper } = await mountHub(true)
    expect(wrapper.findAll('[data-testid^="hub-group-"]')).toHaveLength(7)
    const links = wrapper.findAll('[data-testid^="hub-group-"] a')
    expect(links).toHaveLength(SETTINGS_SECTIONS.length)
    expect(wrapper.find('[data-testid="hub-locked"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="hub-managed-note"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows non-administrators their account sections and the rest as disabled with the reason', async () => {
    const { wrapper } = await mountHub(false)
    const account = wrapper.get('[data-testid="hub-group-account"]')
    expect(account.findAll('a').map((a) => a.attributes('href'))).toEqual([
      '/settings/profile',
      '/settings/password',
      '/settings/tokens?type=personal',
    ])
    const locked = wrapper.findAll('[data-testid="hub-locked"]')
    expect(locked.length).toBeGreaterThan(10)
    expect(locked[0]!.text()).toContain('Requires administrator')
    expect(locked[0]!.find('a').exists()).toBe(false)
    expect(wrapper.get('[data-testid="hub-managed-note"]').text()).toBe('Instance settings are managed by administrators')
    wrapper.unmount()
  })

  it('filters by title and keywords, writes ?q= and announces the count', async () => {
    const { wrapper, router } = await mountHub(true)
    await wrapper.get('#hub-q').setValue('s3 bucket')
    await flushPromises()
    expect(router.currentRoute.value.query.q).toBe('s3 bucket')
    const hrefs = wrapper.findAll('[data-testid="hub-results"] a').map((a) => a.attributes('href'))
    expect(hrefs).toContain('/settings/backends')
    expect(wrapper.get('#hub-count').text()).toMatch(/match/)
    expect(wrapper.find('[data-testid="hub-group-account"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('restores the search from the URL and clears it with Escape', async () => {
    const { wrapper, router } = await mountHub(true, '/settings?q=sso')
    expect((wrapper.get('#hub-q').element as HTMLInputElement).value).toBe('sso')
    expect(wrapper.findAll('[data-testid="hub-results"] a').map((a) => a.attributes('href'))).toContain('/settings/sso')
    await wrapper.get('#hub-q').trigger('keydown', { key: 'Escape' })
    await flushPromises()
    expect(router.currentRoute.value.query.q).toBeUndefined()
    expect(wrapper.find('[data-testid="hub-group-account"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('says so when nothing matches', async () => {
    const { wrapper } = await mountHub(true, '/settings?q=zzzzqq')
    expect(wrapper.find('[data-testid="hub-empty"]').text()).toContain('No settings match “zzzzqq”')
    wrapper.unmount()
  })

  it('tells non-administrators how many matches are managed by administrators', async () => {
    const { wrapper } = await mountHub(false, '/settings?q=sso')
    expect(wrapper.find('[data-testid="hub-empty"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="hub-locked-matches"]').text()).toContain('1 more setting matches')
    wrapper.unmount()
  })
})
