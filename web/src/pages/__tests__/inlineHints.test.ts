// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import PermissionsPage from '../settings/sections/PermissionsPage.vue'
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
