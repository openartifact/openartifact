// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { defineComponent, h } from 'vue'
import { useRepoPackages } from '../useRepoPackages'

const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

const REPOS = ['alpha', 'beta', 'gamma']

/** Package ids per repository; a repository listed in `held` answers only once released. */
function mockApi(held: Record<string, Promise<void>> = {}) {
  const packageCalls: string[] = []
  spyOn(globalThis, 'fetch').mockImplementation((async (input: string) => {
    const url = new URL(String(input), 'http://localhost')
    if (url.pathname === '/api/v1/repositories') {
      return json({ total: REPOS.length, repositories: REPOS.map((name) => ({ name })) })
    }
    const m = /^\/api\/v1\/repositories\/([^/]+)\/packages$/.exec(url.pathname)
    if (m) {
      const repo = m[1]!
      packageCalls.push(repo)
      await held[repo]
      return json({ total: 1, packages: [{ id: `${repo}-pkg`, name: `${repo}-name`, version: '1.0.0', format: 'npm' }] })
    }
    return json({})
  }) as never)
  return packageCalls
}

async function setup() {
  let state!: ReturnType<typeof useRepoPackages>
  const Host = defineComponent({
    setup() {
      state = useRepoPackages()
      return () => h('div')
    },
  })
  mount(Host, { global: { plugins: [createPinia()] } })
  await flushPromises()
  return state
}

describe('useRepoPackages', () => {
  it('lists the repositories and leaves the selection to the form', async () => {
    const calls = mockApi()
    const state = await setup()
    expect(state.repos.value.map((r) => r.name)).toEqual(REPOS)
    expect(state.reposLoaded.value).toBe(true)
    expect(state.selectedRepo.value).toBe('')
    expect(calls).toEqual([])

    state.selectedRepo.value = 'beta'
    await flushPromises()
    expect(state.packages.value.map((p) => p.id)).toEqual(['beta-pkg'])
    expect(state.findPackage('beta-pkg')?.name).toBe('beta-name')
  })

  it('discards the packages of a repository that was deselected before they arrived', async () => {
    let release!: () => void
    const slow = new Promise<void>((resolve) => (release = resolve))
    mockApi({ alpha: slow })
    const state = await setup()
    state.selectedRepo.value = 'alpha'
    await flushPromises()
    expect(state.packages.value).toEqual([])

    state.selectedRepo.value = 'beta'
    await flushPromises()
    expect(state.packages.value.map((p) => p.id)).toEqual(['beta-pkg'])

    release()
    await flushPromises()
    expect(state.packages.value.map((p) => p.id)).toEqual(['beta-pkg'])
  })

  it('loads every package of the repository by cursor', async () => {
    const urls: string[] = []
    spyOn(globalThis, 'fetch').mockImplementation((async (input: string) => {
      const url = new URL(String(input), 'http://localhost')
      urls.push(url.search)
      if (url.pathname === '/api/v1/repositories') return json({ total: 1, repositories: [{ name: 'alpha' }] })
      const cursor = url.searchParams.get('cursor')
      if (!cursor) return json({ total: 2, has_more: true, next_cursor: 'c1', packages: [{ id: 'p1', name: 'a', version: '1', format: 'npm' }] })
      return json({ has_more: false, packages: [{ id: 'p2', name: 'b', version: '1', format: 'npm' }] })
    }) as never)
    const state = await setup()
    state.selectedRepo.value = 'alpha'
    await flushPromises()
    expect(state.packages.value.map((p) => p.id)).toEqual(['p1', 'p2'])
    expect(urls.filter((u) => u.includes('cursor='))).toEqual(['?limit=100&cursor=c1'])
  })
})
