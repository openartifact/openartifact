// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, spyOn } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import RepoPackagesTab from '../RepoPackagesTab.vue'
import type { Repository } from '../../../api/repositories'

async function mountTab(repo: Partial<Repository>) {
  spyOn(globalThis, 'fetch').mockImplementation((async () =>
    new Response(JSON.stringify({ packages: [], total: 0 }), { status: 200, headers: { 'Content-Type': 'application/json' } })) as unknown as typeof fetch)
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { render: () => null } }] })
  await router.push('/')
  const w = mount(RepoPackagesTab, { props: { repo: { id: 'r', name: 'npm-proxy', format: 'npm', ...repo } as Repository }, global: { plugins: [pinia, router] } })
  await flushPromises()
  return w
}

describe('RepoPackagesTab: empty proxy', () => {
  it('teaches the client command for this repository and where the first request goes', async () => {
    const w = await mountTab({ repo_type: 'proxy', upstream_url: 'https://registry.npmjs.org' })
    expect(w.text()).toContain('Nothing cached yet')
    expect(w.text()).toContain('the first request fetches the package from https://registry.npmjs.org and caches it here')
    expect(w.find('[data-testid="state-command"]').text()).toContain(`${window.location.origin}/repos/npm-proxy`)
  })

  it('shows no client command for an empty hosted repository', async () => {
    const w = await mountTab({ repo_type: 'hosted' })
    expect(w.find('[data-testid="state-command"]').exists()).toBe(false)
  })
})
