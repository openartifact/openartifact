// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ProjectsPage from '../ProjectsPage.vue'
import { useToastStore } from '../../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let fetchSpy: Mock<typeof fetch>

async function mountPage(opts: { selfCreate: boolean; putStatus?: number }) {
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const path = new URL(String(input), 'http://localhost').pathname
    if (path === '/api/v1/admin/projects/settings') {
      if (init?.method === 'PUT') {
        if (opts.putStatus) return json({ error: 'boom' }, opts.putStatus)
        return json(JSON.parse(init.body as string))
      }
      return json({ self_create: opts.selfCreate })
    }
    if (path === '/api/v1/projects') return json({ projects: [], can_create: true })
    return json({ items: [], total: 0 })
  })
  const pinia = createPinia()
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div />' } }] })
  await router.push('/')
  const wrapper = mount(ProjectsPage, { global: { plugins: [pinia, router] } })
  await flushPromises()
  return { wrapper, toasts: useToastStore(pinia) }
}

const toggleOf = (wrapper: VueWrapper) =>
  wrapper.find('[data-testid="self-create"] [role="switch"]')

describe('admin ProjectsPage self-service creation', () => {
  it('reflects the stored setting', async () => {
    const { wrapper } = await mountPage({ selfCreate: true })
    expect(wrapper.find('[data-testid="self-create"]').text()).toContain('Allow users to create projects')
    expect(toggleOf(wrapper).attributes('aria-checked')).toBe('true')
  })

  it('saves a change with PUT {self_create} and confirms it', async () => {
    const { wrapper, toasts } = await mountPage({ selfCreate: false })
    expect(toggleOf(wrapper).attributes('aria-checked')).toBe('false')
    await toggleOf(wrapper).trigger('click')
    await flushPromises()
    const put = fetchSpy.mock.calls.find((c) => (c[1] as RequestInit | undefined)?.method === 'PUT')!
    expect(String(put[0])).toBe('/api/v1/admin/projects/settings')
    expect(JSON.parse((put[1] as RequestInit).body as string)).toEqual({ self_create: true })
    expect(toggleOf(wrapper).attributes('aria-checked')).toBe('true')
    expect(toasts.visible.some((x) => x.level === 'success')).toBe(true)
  })

  it('reverts the toggle and shows an error when saving fails', async () => {
    const { wrapper } = await mountPage({ selfCreate: false, putStatus: 500 })
    await toggleOf(wrapper).trigger('click')
    await flushPromises()
    expect(toggleOf(wrapper).attributes('aria-checked')).toBe('false')
    expect(wrapper.find('[data-testid="self-create"] [role="alert"]').text()).toContain('Failed to save')
  })
})
