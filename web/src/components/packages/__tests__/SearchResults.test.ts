// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { defineComponent, h } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import SearchResults from '../SearchResults.vue'
import { useSearch } from '../../../composables/useSearch'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const SHA256 = 'a'.repeat(64)

/** Mounts the results table bound to the real search state, restored from `path`. */
async function mountSearch(path: string, respond: (url: URL) => Response): Promise<{ wrapper: VueWrapper; urls: URL[] }> {
  const urls: URL[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost')
    urls.push(url)
    return respond(url)
  }) as typeof fetch)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { render: () => null } }] })
  await router.push(path)
  const Host = defineComponent({
    setup() {
      const s = useSearch()
      return () => h(SearchResults, { list: s.list, mode: s.mode.value })
    },
  })
  const wrapper = mount(Host, { global: { plugins: [router] } })
  await flushPromises()
  return { wrapper, urls }
}

const group = (name: string, over: Record<string, unknown> = {}) => ({
  name,
  version: '1.0.0',
  format: 'npm',
  repository: 'libs',
  version_count: 1,
  ...over,
})

describe('SearchResults: package mode', () => {
  it('lists one row per package with latest version, version count and links', async () => {
    const { wrapper, urls } = await mountSearch('/?q=left', () =>
      json({ packages: [group('@scope/left-pad', { version: '2.1.0', version_count: 3 }), group('left', { repository: 'mirror/x' })], total: 2 }),
    )
    expect(urls[0].searchParams.get('group')).toBe('package')
    expect(urls[0].searchParams.get('q')).toBe('left')
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('@scope/left-pad')
    expect(rows[0].text()).toContain('2.1.0')
    expect(rows[0].text()).toContain('3 versions')
    expect(rows[0].find('a[href="/repos/libs/packages/@scope/left-pad"]').exists()).toBe(true)
    // The repository link is encoded, the package link keeps nothing of it.
    expect(rows[1].find('a[href="/repos/mirror%2Fx"]').exists()).toBe(true)
    expect(rows[1].text()).toContain('1 version')
  })

  it('offers the version expander only for packages with more than one version and loads them on demand', async () => {
    const { wrapper, urls } = await mountSearch('/?q=left', (url) => {
      if (url.pathname.endsWith('/search')) return json({ packages: [group('multi', { version_count: 2 }), group('single')], total: 2 })
      return json({
        packages: [
          { id: 'a', version: '1.0.0', created_at: '2026-01-01T00:00:00Z' },
          { id: 'b', version: '1.1.0', created_at: '2026-02-01T00:00:00Z' },
          // second file of 1.1.0 must not make a second row
          { id: 'c', version: '1.1.0', created_at: '2026-01-15T00:00:00Z' },
        ],
        total: 3,
      })
    })
    const buttons = wrapper.findAll('tbody button[aria-expanded]')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('[data-testid="version-list"]').exists()).toBe(false)

    await buttons[0].trigger('click')
    await flushPromises()
    expect(urls.length).toBeGreaterThan(1)
    const versions = wrapper.findAll('[data-testid="version-list"] li')
    expect(versions.map((v) => v.find('a').text())).toEqual(['1.1.0', '1.0.0'])
    expect(wrapper.find('tbody button[aria-expanded]').attributes('aria-expanded')).toBe('true')

    await wrapper.find('tbody button[aria-expanded]').trigger('click')
    expect(wrapper.find('[data-testid="version-list"]').exists()).toBe(false)
  })

  it('pages through results 20 at a time and requests the next page from the server', async () => {
    const { wrapper, urls } = await mountSearch('/?q=lib', (url) => {
      const page = Number(url.searchParams.get('page') ?? '1')
      return json({ packages: [group(`pkg-${page}`)], total: 45 })
    })
    expect(urls[0].searchParams.get('limit')).toBe('20')
    expect(wrapper.text()).toContain('pkg-1')
    expect(wrapper.text()).toContain('Page 1 of 3')
    const next = wrapper.findAll('button').find((b) => b.text() === 'Next')!
    await next.trigger('click')
    await flushPromises()
    expect(urls[urls.length - 1].searchParams.get('page')).toBe('2')
    expect(wrapper.text()).toContain('pkg-2')
    expect(wrapper.text()).toContain('Page 2 of 3')
  })

  it('shows the total match count from the server, not the page size', async () => {
    const { wrapper } = await mountSearch('/?q=lib', () => json({ packages: [group('a')], total: 137 }))
    expect(wrapper.find('[data-testid="search-count"]').text()).toContain('137')
  })

  it('shows the no-match state with the query when nothing matches', async () => {
    const { wrapper } = await mountSearch('/?q=nothing-here', () => json({ packages: [], total: 0 }))
    expect(wrapper.findAll('tbody tr')).toHaveLength(0)
    expect(wrapper.text()).toContain('nothing-here')
  })

  it('offers to clear scope filters on an empty scoped result, and re-searches without them', async () => {
    const { wrapper, urls } = await mountSearch('/?q=lib&format=npm&repo=libs&flagged=1', (url) =>
      url.searchParams.get('format') ? json({ packages: [], total: 0 }) : json({ packages: [group('free')], total: 1 }),
    )
    expect(urls[0].searchParams.get('format')).toBe('npm')
    expect(urls[0].searchParams.get('repo')).toBe('libs')
    expect(urls[0].searchParams.get('flagged')).toBe('true')
    const clear = wrapper.findAll('button').find((b) => b.text() === 'Clear filters')!
    await clear.trigger('click')
    await flushPromises()
    const last = urls[urls.length - 1]
    expect(last.searchParams.has('format')).toBe(false)
    expect(last.searchParams.has('repo')).toBe(false)
    expect(last.searchParams.has('flagged')).toBe(false)
    expect(wrapper.text()).toContain('free')
  })

  it('surfaces a failed search with a retry that asks again', async () => {
    let fail = true
    const { wrapper, urls } = await mountSearch('/?q=lib', () => (fail ? json({ error: 'boom' }, 500) : json({ packages: [group('back')], total: 1 })))
    expect(wrapper.text()).toContain('boom')
    fail = false
    const before = urls.length
    await wrapper.findAll('button').find((b) => b.text() === 'Retry')!.trigger('click')
    await flushPromises()
    expect(urls.length).toBe(before + 1)
    expect(wrapper.text()).toContain('back')
  })

  it('requests nothing for an empty query', async () => {
    const { wrapper, urls } = await mountSearch('/', () => json({ packages: [], total: 0 }))
    expect(urls).toHaveLength(0)
    expect(wrapper.findAll('tbody tr')).toHaveLength(0)
  })
})

describe('SearchResults: checksum mode', () => {
  const hit = (over: Record<string, unknown> = {}) => ({
    repository: 'libs',
    format: 'maven',
    name: 'org.acme:core',
    version: '1.0/rc 1',
    filename: 'core-1.0.jar',
    checksum: SHA256,
    algorithm: 'sha256',
    ...over,
  })

  it('treats a digest-shaped query as a checksum lookup and lists file, digest, version and algorithm', async () => {
    const { wrapper, urls } = await mountSearch(`/?q=SHA256:${SHA256.toUpperCase()}`, () => json({ packages: [hit()], total: 1 }))
    expect(urls).toHaveLength(1)
    expect(urls[0].searchParams.get('checksum')).toBe(SHA256)
    expect(urls[0].searchParams.has('q')).toBe(false)
    expect(urls[0].searchParams.has('group')).toBe(false)
    const row = wrapper.find('tbody tr')
    expect(row.text()).toContain('core-1.0.jar')
    expect(row.text()).toContain(SHA256)
    expect(row.text()).toContain('sha256')
    // The version link encodes the awkward version, keeping the slash segment-safe.
    expect(row.find('a[href="/repos/libs/packages/org.acme%3Acore/versions/1.0/rc%201"]').exists()).toBe(true)
    // No version expander in checksum mode.
    expect(wrapper.find('tbody button[aria-expanded]').exists()).toBe(false)
  })

  it('shows the checksum empty state when no file has that digest', async () => {
    const { wrapper } = await mountSearch(`/?q=${SHA256}`, () => json({ packages: [], total: 0 }))
    expect(wrapper.findAll('tbody tr')).toHaveLength(0)
    expect(wrapper.text()).not.toContain(SHA256)
  })

  it('does not call the server for checksum mode with a malformed digest', async () => {
    const { urls } = await mountSearch('/?q=abc123&mode=checksum', () => json({ packages: [], total: 0 }))
    expect(urls).toHaveLength(0)
  })

  it('an explicit name mode searches by name even for a digest-shaped query', async () => {
    const { urls } = await mountSearch(`/?q=${SHA256}&mode=name`, () => json({ packages: [], total: 0 }))
    expect(urls[0].searchParams.get('q')).toBe(SHA256)
    expect(urls[0].searchParams.get('group')).toBe('package')
  })
})
