// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import AppHeader from '../AppHeader.vue'
import AppSidebar from '../AppSidebar.vue'

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'dashboard', component: { template: '<div />' } },
      { path: '/repos', name: 'repos', component: { template: '<div />' } },
      { path: '/projects', name: 'projects', component: { template: '<div />' } },
      { path: '/search', name: 'search', component: { template: '<div />' } },
      { path: '/login', name: 'login', component: { template: '<div />' } },
      { path: '/settings/profile', name: 'settings-profile', component: { template: '<div />' } },
      { path: '/settings/theme', name: 'settings-theme', component: { template: '<div />' } },
      { path: '/admin/users', name: 'admin-users', component: { template: '<div />' } },
      { path: '/admin/groups', name: 'admin-groups', component: { template: '<div />' } },
      { path: '/admin/permissions', name: 'admin-permissions', component: { template: '<div />' } },
      { path: '/admin/sso', name: 'admin-sso', component: { template: '<div />' } },
      { path: '/admin/gpg', name: 'admin-gpg', component: { template: '<div />' } },
      { path: '/admin/tokens', name: 'admin-tokens', component: { template: '<div />' } },
      { path: '/admin/audit', name: 'admin-audit', component: { template: '<div />' } },
      { path: '/admin/storage', name: 'admin-storage', component: { template: '<div />' } },
      { path: '/admin/storage/tiers', name: 'admin-storage-tiers', component: { template: '<div />' } },
      { path: '/admin/system', name: 'admin-system', component: { template: '<div />' } },
      { path: '/admin/projects', name: 'admin-projects', component: { template: '<div />' } },
      { path: '/admin/backup', name: 'admin-backup', component: { template: '<div />' } },
      { path: '/admin/webhooks', name: 'admin-webhooks', component: { template: '<div />' } },
      { path: '/admin/ai', name: 'admin-ai', component: { template: '<div />' } },
      { path: '/admin/branding', name: 'admin-branding', component: { template: '<div />' } },
      { path: '/stats', name: 'stats', component: { template: '<div />' } },
      { path: '/security/vulns', name: 'security-vulns', component: { template: '<div />' } },
      { path: '/security/licenses', name: 'security-licenses', component: { template: '<div />' } },
      { path: '/security/secrets', name: 'security-secrets', component: { template: '<div />' } },
      { path: '/security/ai-analysis', name: 'security-ai-analysis', component: { template: '<div />' } },
    ],
  })
}

describe('App shell snapshots', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.documentElement.classList.remove('dark')
  })

  it('AppHeader uses semantic tokens (no hard-coded grays/whites)', async () => {
    const router = makeRouter()
    await router.push('/')
    await router.isReady()
    const wrapper = mount(AppHeader, {
      global: { plugins: [router] },
    })
    const html = wrapper.html()
    expect(html).not.toMatch(/\bbg-white\b/)
    expect(html).not.toMatch(/\bbg-gray-\d+\b/)
    expect(html).not.toMatch(/\btext-gray-\d+\b/)
    expect(html).not.toMatch(/\bborder-gray-\d+\b/)
  })

  it('AppHeader renders identically in light and dark mode (HTML only; theme is via CSS vars)', async () => {
    const router = makeRouter()
    await router.push('/')
    await router.isReady()
    const lightWrapper = mount(AppHeader, { global: { plugins: [router] } })
    const lightHtml = lightWrapper.html()

    document.documentElement.classList.add('dark')
    const darkWrapper = mount(AppHeader, { global: { plugins: [router] } })
    const darkHtml = darkWrapper.html()
    document.documentElement.classList.remove('dark')

    // Because we use CSS custom properties, the markup itself does not
    // change between modes — only the resolved colors do.
    expect(darkHtml).toEqual(lightHtml)
  })

  it('AppSidebar uses semantic tokens', async () => {
    const router = makeRouter()
    await router.push('/')
    await router.isReady()
    const wrapper = mount(AppSidebar, {
      global: { plugins: [router] },
    })
    const html = wrapper.html()
    expect(html).not.toMatch(/\bbg-white\b/)
    expect(html).not.toMatch(/\bbg-gray-\d+\b/)
    expect(html).not.toMatch(/\btext-gray-\d+\b/)
  })
})
