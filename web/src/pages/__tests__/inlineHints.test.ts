// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import RepoWizardPage from '../RepoWizardPage.vue'
import PermissionsPage from '../admin/PermissionsPage.vue'
import NewRepoModal from '../../components/NewRepoModal.vue'
import { useSessionStore, type User } from '../../stores/session'

const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

function serveEmpty() {
  spyOn(globalThis, 'fetch').mockImplementation((async () => json({ items: [], repositories: [], total: 0 })) as never)
}

async function router(path: string) {
  const r = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }],
  })
  await r.push(path)
  await r.isReady()
  return r
}

const IMMUTABLE = 'Format and type cannot be changed after creation.'

describe('repository format and type are immutable', () => {
  // Verified against the API: PATCH /api/v1/repository/{name} has no format or repo_type field.
  it('the wizard says so on the format and type steps while creating', async () => {
    serveEmpty()
    setActivePinia(createPinia())
    useSessionStore().user = { id: 'a', username: 'root', is_admin: true } as User
    const w = mount(RepoWizardPage, { global: { plugins: [await router('/repos/new')] } })
    await flushPromises()
    expect(w.find('[data-testid="immutable-hint"]').exists()).toBe(false)

    await w.get('#repo-name').setValue('my-repo')
    const next = () => w.findAll('button').find((b) => b.text() === 'Next')!
    await next().trigger('click')
    expect(w.get('[data-testid="immutable-hint"]').text()).toBe(IMMUTABLE)

    await next().trigger('click')
    expect(w.get('[data-testid="immutable-hint"]').text()).toBe(IMMUTABLE)
  })

  it('the new-repository dialog says so next to the format and type choices', async () => {
    serveEmpty()
    setActivePinia(createPinia())
    useSessionStore().user = { id: 'a', username: 'root', is_admin: true } as User
    mount(NewRepoModal, { props: { modelValue: true }, attachTo: document.body, global: { plugins: [await router('/repos')] } })
    await flushPromises()
    expect(document.body.querySelector('[data-testid="immutable-hint"]')?.textContent).toBe(IMMUTABLE)
  })
})

describe('permission target patterns', () => {
  // Verified against auth.CompilePattern: anchored, "*" is ".*", an empty pattern is rejected.
  it('the form states anchoring and that an empty pattern is not accepted', async () => {
    serveEmpty()
    setActivePinia(createPinia())
    const w = mount(PermissionsPage, { attachTo: document.body, global: { plugins: [await router('/admin/permissions')] } })
    await flushPromises()
    await w.findAll('button').find((b) => b.text() === 'Create permission')!.trigger('click')
    await flushPromises()
    const hints = [...document.body.querySelectorAll('p')].map((p) => p.textContent ?? '').filter((s) => s.includes('whole value'))
    expect(hints).toHaveLength(2)
    for (const hint of hints) {
      expect(hint).toContain('"dev" does not match "devops"')
      expect(hint).toContain('Required')
      expect(hint).toContain('"*" alone matches everything')
      expect(hint).not.toContain('leave empty')
    }
  })
})
