// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The artifact browser pages, searches and orders on the server: it must never
// walk the whole listing, so a repository of any size shows every package.
import { describe, it, expect, beforeEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ArtifactBrowserPage from '../ArtifactBrowserPage.vue'
import BulkActionsBar from '../../components/BulkActionsBar.vue'
import { useToastStore } from '../../stores/toast'

const TOTAL = 130

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let fetchSpy: Mock<typeof fetch>
let matching: number

/** Package listing URLs the page asked for, in order. */
const listings = () =>
  fetchSpy.mock.calls.map((c) => String(c[0])).filter((u) => u.includes('/packages?'))

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

async function mountPage() {
  setActivePinia(createPinia())
  matching = TOTAL
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async (input: string) => {
    const url = new URL(String(input), 'http://localhost')
    if (url.pathname === '/api/v1/repository/big') return json({ name: 'big', format: 'raw', type: 'hosted' })
    if (url.pathname === '/api/v1/repositories/big/packages') {
      const page = Number(url.searchParams.get('page') ?? '1')
      const limit = Number(url.searchParams.get('limit') ?? '25')
      const total = url.searchParams.get('q') ? 3 : matching
      const count = Math.max(0, Math.min(limit, total - (page - 1) * limit))
      return json({
        total,
        packages: Array.from({ length: count }, (_, i) => ({
          id: `p${page}-${i}`,
          name: `pkg-${(page - 1) * limit + i}`,
          version: '1.0.0',
          format: 'raw',
        })),
      })
    }
    return json({})
  }) as never)

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/repos/:name/browse', component: ArtifactBrowserPage },
      { path: '/repos/:rest(.*)*', component: { template: '<div />' } },
    ],
  })
  await router.push('/repos/big/browse')
  await router.isReady()
  const wrapper = mount(ArtifactBrowserPage, { global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

describe('ArtifactBrowserPage', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('asks the server for one page at a time, ordered by name', async () => {
    const wrapper = await mountPage()

    expect(listings()).toEqual(['/api/v1/repositories/big/packages?page=1&limit=25&sort=name'])
    expect(wrapper.findAll('a[href*="/packages/raw/"]')).toHaveLength(25)
    expect(wrapper.text()).toContain('1–25')
    expect(wrapper.text()).toContain(String(TOTAL))
  })

  it('pages through the whole listing on the server', async () => {
    const wrapper = await mountPage()

    await wrapper.find('button[aria-label="Next"]').trigger('click')
    await flushPromises()

    expect(listings().at(-1)).toBe('/api/v1/repositories/big/packages?page=2&limit=25&sort=name')
    expect(wrapper.text()).toContain('pkg-25')

    for (let i = 0; i < 4; i++) {
      await wrapper.find('button[aria-label="Next"]').trigger('click')
      await flushPromises()
    }
    expect(listings().at(-1)).toBe('/api/v1/repositories/big/packages?page=6&limit=25&sort=name')
    expect(wrapper.findAll('a[href*="/packages/raw/"]')).toHaveLength(TOTAL - 125)
  })

  it('searches on the server after a typing pause and restarts at page 1', async () => {
    const wrapper = await mountPage()
    await wrapper.find('button[aria-label="Next"]').trigger('click')
    await flushPromises()
    const before = listings().length

    const input = wrapper.find('input[type="search"], input[type="text"]')
    await input.setValue('p')
    await input.setValue('pk')
    await sleep(50)
    expect(listings()).toHaveLength(before) // still typing: nothing sent yet

    await sleep(400)
    await flushPromises()

    expect(listings()).toHaveLength(before + 1)
    expect(listings().at(-1)).toBe('/api/v1/repositories/big/packages?page=1&limit=25&q=pk&sort=name')
    expect(wrapper.findAll('a[href*="/packages/raw/"]')).toHaveLength(3)
  })

  it('orders by upload time when the sort button is toggled', async () => {
    const wrapper = await mountPage()

    const sort = wrapper.findAll('button').find((b) => b.text().includes('Sort'))!
    await sort.trigger('click')
    await flushPromises()

    expect(listings().at(-1)).toBe('/api/v1/repositories/big/packages?page=1&limit=25&sort=created')
  })

  it('reports no "Deleted 0" success when nothing was deleted, and reloads the list', async () => {
    const wrapper = await mountPage()
    const before = listings().length

    wrapper.findComponent(BulkActionsBar).vm.$emit('deleted', [])
    await flushPromises()

    expect(useToastStore().queue).toHaveLength(0)
    expect(listings()).toHaveLength(before + 1)
  })

  it('steps back to the last page that still exists after deleting its rows', async () => {
    const wrapper = await mountPage()
    for (let i = 0; i < 5; i++) {
      await wrapper.find('button[aria-label="Next"]').trigger('click')
      await flushPromises()
    }
    expect(wrapper.findAll('a[href*="/packages/raw/"]')).toHaveLength(TOTAL - 125)

    matching = 125 // the five rows of the last page were deleted
    wrapper.findComponent(BulkActionsBar).vm.$emit('deleted', ['a', 'b', 'c', 'd', 'e'])
    await flushPromises()

    expect(listings().at(-1)).toBe('/api/v1/repositories/big/packages?page=5&limit=25&sort=name')
    expect(wrapper.findAll('a[href*="/packages/raw/"]')).toHaveLength(25)
  })
})
