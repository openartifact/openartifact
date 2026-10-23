// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// AppSidebar tests.
// The component runs against the real session and UI stores, each living in a
// per-test Pinia, so state changes are tracked by the template exactly as in
// the app. Only the logout API call is stubbed, with spyOn() on the module's
// export (reverted by mock.restore() in the global afterEach, see
// test/isolation.ts) — mock.module() would instead replace the module for every
// test file that runs afterwards.
import { describe, it, expect, beforeEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { createPinia, setActivePinia, type StoreGeneric } from 'pinia'
import * as authApi from '../../api/auth'
import { ApiError } from '../../api/client'
import { useToastStore } from '../../stores/toast'
import { useSessionStore, type User } from '../../stores/session'
import { useUIStore } from '../../stores/ui'
import AppSidebar from '../AppSidebar.vue'

let session: StoreGeneric
let ui: StoreGeneric
let toasts: StoreGeneric
let mockLogout: Mock<typeof authApi.logout>
let sessionActions: string[]

function makeUser(overrides: Record<string, unknown> = {}): User {
  return { id: 'u1', username: 'testuser', is_admin: false, groups: [], ...overrides } as User
}

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'dashboard', component: { template: '<div/>' } },
      { path: '/login', name: 'login', component: { template: '<div/>' } },
      { path: '/repos', name: 'repos', component: { template: '<div/>' } },
      { path: '/projects', name: 'projects', component: { template: '<div/>' } },
      { path: '/search', name: 'search', component: { template: '<div/>' } },
      { path: '/stats', name: 'stats', component: { template: '<div/>' } },
      { path: '/admin/users', name: 'admin-users', component: { template: '<div/>' } },
      { path: '/admin/groups', name: 'admin-groups', component: { template: '<div/>' } },
      { path: '/admin/projects', name: 'admin-projects', component: { template: '<div/>' } },
      { path: '/admin/permissions', name: 'admin-permissions', component: { template: '<div/>' } },
      { path: '/admin/sso', name: 'admin-sso', component: { template: '<div/>' } },
      { path: '/admin/gpg', name: 'admin-gpg', component: { template: '<div/>' } },
      { path: '/admin/tokens', name: 'admin-tokens', component: { template: '<div/>' } },
      { path: '/admin/audit', name: 'admin-audit', component: { template: '<div/>' } },
      { path: '/admin/storage', name: 'admin-storage', component: { template: '<div/>' } },
      { path: '/admin/storage/tiers', name: 'admin-storage-tiers', component: { template: '<div/>' } },
      { path: '/admin/backup', name: 'admin-backup', component: { template: '<div/>' } },
      { path: '/admin/webhooks', name: 'admin-webhooks', component: { template: '<div/>' } },
      { path: '/admin/system', name: 'admin-system', component: { template: '<div/>' } },
      { path: '/admin/dependency-track', name: 'admin-dependency-track', component: { template: '<div/>' } },
      { path: '/admin/ai', name: 'admin-ai', component: { template: '<div/>' } },
      { path: '/security/vulns', name: 'security-vulns', component: { template: '<div/>' } },
      { path: '/security/licenses', name: 'security-licenses', component: { template: '<div/>' } },
      { path: '/security/secrets', name: 'security-secrets', component: { template: '<div/>' } },
      { path: '/security/ai-analysis', name: 'security-ai', component: { template: '<div/>' } },
      { path: '/settings/profile', name: 'settings-profile', component: { template: '<div/>' } },
    ],
  })
}

function mountSidebar(router = makeRouter()) {
  return { wrapper: mount(AppSidebar, { global: { plugins: [router] } }), router }
}

describe('AppSidebar', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    session = useSessionStore()
    ui = useUIStore()
    toasts = useToastStore()
    sessionActions = []
    session.$onAction(({ name }) => {
      sessionActions.push(name)
    })
    // The sidebar reads the server version from /system/info; keep the network out of it.
    spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ version: '9.9.9' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )
    mockLogout = spyOn(authApi, 'logout').mockResolvedValue(undefined)
  })

  // Browse links
  it('renders Browse section links', () => {
    const { wrapper } = mountSidebar()
    const text = wrapper.text()
    expect(text).toContain('Dashboard')
    expect(text).toContain('Repositories')
    expect(text).toContain('Search')
    expect(text).not.toContain('Statistics')
  })

  it('links Projects for every authenticated user, not only admins', () => {
    session.user = makeUser({ is_admin: false })
    const { wrapper } = mountSidebar()
    expect(wrapper.text()).toContain('Projects')
    expect(wrapper.find('a[href="/projects"]').exists()).toBe(true)
    // The admin area keeps its own entry, hidden for non-admins.
    expect(wrapper.html()).not.toContain('/admin/projects')
  })

  it('renders Security section links for admins', () => {
    session.user = makeUser({ is_admin: true })
    const { wrapper } = mountSidebar()
    expect(wrapper.text()).toContain('Vulnerabilities')
    expect(wrapper.text()).toContain('Licenses')
    expect(wrapper.text()).toContain('Secrets')
    expect(wrapper.find('a[href="/security/ai-analysis"]').exists()).toBe(true)
  })

  it('hides the Security section from non-admins (the scan APIs need manage rights)', () => {
    session.user = makeUser({ is_admin: false })
    const { wrapper } = mountSidebar()
    expect(wrapper.text()).not.toContain('Vulnerabilities')
    expect(wrapper.html()).not.toContain('/security/')
  })

  it('renders Settings section link', () => {
    const { wrapper } = mountSidebar()
    expect(wrapper.text()).toContain('Profile')
  })

  // Admin visibility — controlled by reactive session.isAdmin
  it('shows Admin section when user.is_admin is true', async () => {
    session.user = makeUser({ is_admin: true })
    const { wrapper } = mountSidebar()
    expect(wrapper.html()).toContain('/admin/users')
  })

  it('links Storage Tiers next to Storage Backends and highlights only the matching one', async () => {
    session.user = makeUser({ is_admin: true })
    const { wrapper, router } = mountSidebar()
    await router.push('/admin/storage/tiers')
    await flushPromises()
    expect(wrapper.text()).toContain('Storage Tiers')
    expect(wrapper.find('a[href="/admin/storage/tiers"]').classes()).toContain('bg-surface-hover')
    expect(wrapper.find('a[href="/admin/storage"]').classes()).not.toContain('bg-surface-hover')
  })

  it('hides Admin section when user is not admin', async () => {
    session.user = makeUser({ is_admin: false })
    const { wrapper } = mountSidebar()
    expect(wrapper.html()).not.toContain('/admin/users')
  })

  it('shows Admin section when user belongs to admin group', async () => {
    session.user = makeUser({ is_admin: false, groups: [{ name: 'admin' }] })
    const { wrapper } = mountSidebar()
    expect(wrapper.html()).toContain('/admin/users')
  })

  it('hides Admin section when user is null', async () => {
    session.user = null  // already null from beforeEach
    const { wrapper } = mountSidebar()
    expect(wrapper.html()).not.toContain('/admin/users')
  })

  // Avatar/username
  it('displays username in footer', async () => {
    session.user = makeUser({ username: 'testuser' })
    const { wrapper } = mountSidebar()
    expect(wrapper.text()).toContain('testuser')
  })

  it('renders first-letter avatar from username', async () => {
    session.user = makeUser({ username: 'bob' })
    const { wrapper } = mountSidebar()
    const avatar = wrapper.find('.rounded-full span')
    expect(avatar.text()).toBe('B')
  })

  it('renders "?" avatar when user is null', async () => {
    session.user = null
    const { wrapper } = mountSidebar()
    const avatar = wrapper.find('.rounded-full span')
    expect(avatar.text()).toBe('?')
  })

  // Logout
  it('calls apiLogout, clears session, and navigates to /login', async () => {
    session.user = makeUser({ username: 'test' })
    const { wrapper, router } = mountSidebar()
    const logoutBtn = wrapper.find('button[aria-label="Sign out"]')
    expect(logoutBtn.exists()).toBe(true)
    await logoutBtn.trigger('click')
    await flushPromises()
    expect(mockLogout).toHaveBeenCalled()
    expect(sessionActions).toEqual(['logout'])
    expect(session.user).toBeNull()
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('stays signed in and reports the failure when apiLogout throws', async () => {
    mockLogout.mockRejectedValueOnce(new Error('network fail'))
    session.user = makeUser({ username: 'test' })
    const { wrapper, router } = mountSidebar()
    await router.push('/repos')
    await wrapper.find('button[aria-label="Sign out"]').trigger('click')
    await flushPromises()
    expect(session.user).not.toBeNull()
    expect(router.currentRoute.value.path).toBe('/repos')
    expect(toasts.queue.map((x: { title: string }) => x.title)).toContain('Could not sign out')
  })

  it('signs out locally when the server says the session is already gone', async () => {
    mockLogout.mockRejectedValueOnce(new ApiError('Unauthorized', 401))
    session.user = makeUser({ username: 'test' })
    const { wrapper, router } = mountSidebar()
    await wrapper.find('button[aria-label="Sign out"]').trigger('click')
    await flushPromises()
    expect(session.user).toBeNull()
    expect(router.currentRoute.value.path).toBe('/login')
  })

  // Collapse toggle
  it('toggles collapsed state when toggle button clicked', async () => {
    ui.sidebarCollapsed = false
    const { wrapper } = mountSidebar()
    const btn = wrapper.find('button[aria-label="Collapse sidebar"]')
    expect(btn.exists()).toBe(true)
    await btn.trigger('click')
    expect(ui.sidebarCollapsed).toBe(true)
  })

  it('shows expand button when sidebar is already collapsed', () => {
    ui.sidebarCollapsed = true
    const { wrapper } = mountSidebar()
    expect(wrapper.find('button[aria-label="Expand sidebar"]').exists()).toBe(true)
  })

  it('applies collapsed width class when collapsed', () => {
    ui.sidebarCollapsed = true
    const { wrapper } = mountSidebar()
    expect(wrapper.find('aside').classes()).toContain('w-16')
  })

  it('applies expanded width class when not collapsed', () => {
    ui.sidebarCollapsed = false
    const { wrapper } = mountSidebar()
    expect(wrapper.find('aside').classes()).toContain('w-60')
  })

  it('hides username text when collapsed', () => {
    session.user = makeUser({ username: 'testuser' })
    ui.sidebarCollapsed = true
    const { wrapper } = mountSidebar()
    expect(wrapper.text()).not.toContain('testuser')
  })
})
