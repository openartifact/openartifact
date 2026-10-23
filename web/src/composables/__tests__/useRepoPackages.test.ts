// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { defineComponent, h } from 'vue'
import { LAST_REPO_KEY, useRepoPackages, type RepoPackagesOptions } from '../useRepoPackages'

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

async function setup(path: string, options?: RepoPackagesOptions) {
  const router: Router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }],
  })
  await router.push(path)
  await router.isReady()
  let state!: ReturnType<typeof useRepoPackages>
  const Host = defineComponent({
    setup() {
      state = useRepoPackages(options)
      return () => h('div')
    },
  })
  mount(Host, { global: { plugins: [createPinia(), router] } })
  await flushPromises()
  return { state, router }
}

describe('useRepoPackages initial selection', () => {
  it('prefers a valid repo from the URL over the remembered and the first repository', async () => {
    mockApi()
    localStorage.setItem(LAST_REPO_KEY, 'beta')
    const { state } = await setup('/security/secrets?repo=gamma')
    expect(state.selectedRepo.value).toBe('gamma')
  })

  it('falls back to the last used repository when the URL repo is unknown', async () => {
    mockApi()
    localStorage.setItem(LAST_REPO_KEY, 'beta')
    const { state, router } = await setup('/security/secrets?repo=ghost')
    expect(state.selectedRepo.value).toBe('beta')
    expect(router.currentRoute.value.query.repo).toBe('beta')
  })

  it('ignores a remembered repository that no longer exists and takes the first one', async () => {
    mockApi()
    localStorage.setItem(LAST_REPO_KEY, 'deleted')
    const { state } = await setup('/security/secrets')
    expect(state.selectedRepo.value).toBe('alpha')
  })

  it('takes the first repository when nothing is known, and writes it to the URL and storage', async () => {
    mockApi()
    const { state, router } = await setup('/security/secrets?tab=x')
    expect(state.selectedRepo.value).toBe('alpha')
    expect(router.currentRoute.value.query).toEqual({ tab: 'x', repo: 'alpha' })
    expect(localStorage.getItem(LAST_REPO_KEY)).toBe('alpha')
    expect(state.packages.value.map((p) => p.id)).toEqual(['alpha-pkg'])
  })

  it('leaves the selection to the page in manual mode', async () => {
    const calls = mockApi()
    localStorage.setItem(LAST_REPO_KEY, 'beta')
    const { state, router } = await setup('/security/vulns?repo=gamma', { selection: 'manual' })
    expect(state.repos.value.map((r) => r.name)).toEqual(REPOS)
    expect(state.selectedRepo.value).toBe('')
    expect(calls).toEqual([])

    state.selectedRepo.value = 'beta'
    await flushPromises()
    expect(router.currentRoute.value.query.repo).toBe('gamma')
    expect(localStorage.getItem(LAST_REPO_KEY)).toBe('beta')
    expect(state.packages.value.map((p) => p.id)).toEqual(['beta-pkg'])
  })
})

describe('useRepoPackages selection sync', () => {
  it('writes a manual selection to the URL with replace and to storage', async () => {
    mockApi()
    const { state, router } = await setup('/security/licenses')
    const position = router.options.history.state.position

    state.selectedRepo.value = 'gamma'
    await flushPromises()
    expect(router.currentRoute.value.query.repo).toBe('gamma')
    expect(router.options.history.state.position).toBe(position)
    expect(localStorage.getItem(LAST_REPO_KEY)).toBe('gamma')

    state.selectedRepo.value = ''
    await flushPromises()
    expect(router.currentRoute.value.query.repo).toBeUndefined()
  })

  it('follows a URL change to another known repository and ignores an unknown one', async () => {
    mockApi()
    const { state, router } = await setup('/security/licenses?repo=alpha')
    await router.replace({ query: { repo: 'beta' } })
    await flushPromises()
    expect(state.selectedRepo.value).toBe('beta')

    await router.replace({ query: { repo: 'ghost' } })
    await flushPromises()
    expect(state.selectedRepo.value).toBe('beta')
  })

  it('discards the packages of a repository that was deselected before they arrived', async () => {
    let release!: () => void
    const slow = new Promise<void>((resolve) => (release = resolve))
    mockApi({ alpha: slow })
    const { state } = await setup('/security/licenses')
    expect(state.selectedRepo.value).toBe('alpha')
    expect(state.packages.value).toEqual([])

    state.selectedRepo.value = 'beta'
    await flushPromises()
    expect(state.packages.value.map((p) => p.id)).toEqual(['beta-pkg'])

    release()
    await flushPromises()
    expect(state.packages.value.map((p) => p.id)).toEqual(['beta-pkg'])
  })
})
