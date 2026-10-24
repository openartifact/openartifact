// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { Component } from 'vue'
import SSOPage from '../admin/SSOPage.vue'
import PermissionsPage from '../admin/PermissionsPage.vue'
import AdminTokensPage from '../admin/TokensPage.vue'
import AdminProjectsPage from '../admin/ProjectsPage.vue'
import WebhooksPage from '../admin/WebhooksPage.vue'
import SettingsTokensPage from '../settings/TokensPage.vue'
import ProjectListPage from '../ProjectListPage.vue'
import TrashPage from '../TrashPage.vue'
import { useSessionStore, type User } from '../../stores/session'

const BASE = 'https://docs.example.com/v1.2/'
const json = (body: unknown): Response =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

// Every list endpoint answers empty; /system/info carries the docs base unless disabled.
function serve(info: Record<string, unknown>) {
  return spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL) =>
    String(input) === '/api/v1/system/info'
      ? json(info)
      : json({ items: [], total: 0, providers: [], webhooks: [], tokens: [], projects: [], can_create: false })) as never)
}

async function mountEmpty(page: Component, routeName: string, path: string) {
  setActivePinia(createPinia())
  useSessionStore().user = { id: 'a', username: 'root', is_admin: true } as User
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path, name: routeName, component: page }, { path: '/:rest(.*)*', component: { template: '<div />' } }],
  })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(page, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return wrapper
}

const CASES: { name: string; page: Component; route: string; path: string; docs: string; label: string }[] = [
  { name: 'no SSO providers', page: SSOPage, route: 'admin-sso', path: '/admin/sso', docs: 'security/sso', label: 'Set up single sign-on' },
  { name: 'no permission targets', page: PermissionsPage, route: 'admin-permissions', path: '/admin/permissions', docs: 'security/rbac', label: 'How permission targets work' },
  { name: 'no API tokens (admin)', page: AdminTokensPage, route: 'admin-tokens', path: '/admin/tokens', docs: 'security/authentication', label: 'About API tokens' },
  { name: 'no API tokens (settings)', page: SettingsTokensPage, route: 'settings-tokens', path: '/settings/tokens', docs: 'security/authentication', label: 'About API tokens' },
  { name: 'no projects (admin)', page: AdminProjectsPage, route: 'admin-projects', path: '/admin/projects', docs: 'security/projects', label: 'About projects' },
  { name: 'no projects', page: ProjectListPage, route: 'projects', path: '/projects', docs: 'security/projects', label: 'About projects' },
  { name: 'no webhooks', page: WebhooksPage, route: 'admin-webhooks', path: '/admin/webhooks', docs: 'operations/webhooks', label: 'Webhook events and signatures' },
  { name: 'empty trash', page: TrashPage, route: 'admin-trash', path: '/admin/trash', docs: 'operations/trash', label: 'How the trash and retention work' },
]

describe('empty states link to the matching docs page', () => {
  for (const c of CASES) {
    it(`${c.name}`, async () => {
      serve({ version: '1.2.3', docs_url: BASE })
      const w = await mountEmpty(c.page, c.route, c.path)
      const link = w.get('[data-testid="docs-link"]')
      expect(link.attributes('href')).toBe(`${BASE}${c.docs}`)
      expect(link.attributes('target')).toBe('_blank')
      expect(link.attributes('rel')).toBe('noopener')
      expect(link.text()).toBe(c.label)
    })

    it(`${c.name}: no link when docs links are disabled`, async () => {
      serve({ version: '1.2.3' })
      const w = await mountEmpty(c.page, c.route, c.path)
      expect(w.find('[data-testid="docs-link"]').exists()).toBe(false)
    })
  }
})
