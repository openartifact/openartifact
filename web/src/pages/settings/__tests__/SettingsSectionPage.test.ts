// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, spyOn } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import SettingsSectionPage from '../SettingsSectionPage.vue'
import { useSessionStore, type User } from '../../../stores/session'

let fetchSpy: ReturnType<typeof spyOn>

async function mountSection(path: string, isAdmin: boolean) {
  setActivePinia(createPinia())
  useSessionStore().user = { id: 'u', username: 'dev', is_admin: isAdmin } as User
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/settings', component: { render: () => null } },
      { path: '/settings/:section', name: 'settings-section', component: SettingsSectionPage },
    ],
  })
  await router.push(path)
  const wrapper = mount(SettingsSectionPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return { router, wrapper }
}

describe('SettingsSectionPage', () => {
  beforeEach(() => {
    document.body.replaceChildren()
    fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async () =>
      new Response(JSON.stringify({}), { status: 200, headers: { 'Content-Type': 'application/json' } })) as never)
  })
  afterEach(() => fetchSpy.mockRestore())

  it('shows the breadcrumb Settings > Group above the section title', async () => {
    const { wrapper } = await mountSection('/settings/backends', true)
    const crumbs = wrapper.findAll('header nav ol li').map((li) => li.text())
    expect(crumbs).toEqual(['Settings', 'Storage'])
    expect(wrapper.get('h1').text()).toBe('Storage backends')
    wrapper.unmount()
  })

  it('puts the open group first and expanded in the rail, others collapsed', async () => {
    const { wrapper } = await mountSection('/settings/backends', true)
    const groups = wrapper.findAll('[data-testid="settings-rail"] details')
    expect(groups[0]!.attributes('data-testid')).toBe('rail-group-storage')
    expect(groups[0]!.attributes('open')).toBeDefined()
    expect(groups[1]!.attributes('open')).toBeUndefined()
    const current = wrapper.get('[data-testid="settings-rail"] [aria-current="page"]')
    expect(current.text()).toBe('Storage backends')
    wrapper.unmount()
  })

  it('offers the same sections in a select for small screens and navigates with it', async () => {
    const { wrapper, router } = await mountSection('/settings/backends', true)
    const select = wrapper.get('[data-testid="settings-jump"]')
    expect(select.findAll('optgroup').map((g) => g.attributes('label'))[0]).toBe('Storage')
    await select.setValue('trash')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/settings/trash')
    wrapper.unmount()
  })

  it('refuses an administrator section for everybody else', async () => {
    const { wrapper } = await mountSection('/settings/users', false)
    expect(wrapper.get('[data-testid="section-forbidden"]').text()).toContain('Users requires an administrator')
    expect(wrapper.text()).toContain('dev')
    // the rail lists only what this role can open
    expect(wrapper.findAll('[data-testid="settings-rail"] a').map((a) => a.text())).not.toContain('Users')
    wrapper.unmount()
  })

  it('lists personal tokens under Your account for non-administrators', async () => {
    const { wrapper } = await mountSection('/settings/profile', false)
    expect(wrapper.findAll('[data-testid="settings-rail"] details')).toHaveLength(1)
    expect(wrapper.findAll('header nav ol li').map((li) => li.text())).toEqual(['Settings', 'Your account'])
    wrapper.unmount()
  })

  it('says so for an unknown section', async () => {
    const { wrapper } = await mountSection('/settings/nope', true)
    expect(wrapper.text()).toContain('Unknown setting')
    wrapper.unmount()
  })
})
