// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The sidebar runs against the real session and UI stores, each living in a
// per-test Pinia. Sign-out lives in the header's user menu (UserMenu.test.ts).
import { describe, it, expect, beforeEach } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { createPinia, setActivePinia, type StoreGeneric } from 'pinia'
import { useSessionStore, type User } from '../../stores/session'
import { useUIStore } from '../../stores/ui'
import { useBrandingStore } from '../../stores/branding'
import AppSidebar from '../AppSidebar.vue'

let session: StoreGeneric
let ui: StoreGeneric

const makeUser = (overrides: Record<string, unknown> = {}): User =>
  ({ id: 'u1', username: 'testuser', is_admin: false, groups: [], ...overrides }) as User

function makeRouter() {
  const page = { template: '<div/>' }
  return createRouter({
    history: createMemoryHistory(),
    routes: ['/', '/repos', '/repos/:repo', '/repos/:repo/packages/:pkg(.+)', '/packages', '/projects', '/bundles', '/security', '/settings/:section?'].map((path) => ({ path, component: page })),
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
    ui.sidebarCollapsed = false
  })

  it('renders the seven areas as links in a labelled primary navigation', () => {
    session.user = makeUser({ is_admin: true })
    const { wrapper } = mountSidebar()
    expect(wrapper.find('nav').attributes('aria-label')).toBe('Primary')
    const hrefs = wrapper.findAll('nav a').map((a) => a.attributes('href'))
    expect(hrefs).toEqual(['/', '/repos', '/packages', '/projects', '/bundles', '/security', '/settings'])
    expect(wrapper.text()).toContain('Repositories')
    expect(wrapper.text()).not.toContain('Requires administrator')
  })

  it('keeps Bundles visible but disabled, with the reason, for a developer; Security stays open (server-scoped)', () => {
    session.user = makeUser({ is_admin: false })
    const { wrapper } = mountSidebar()
    const el = wrapper.get('[data-testid="nav-bundles"]')
    expect(el.element.tagName).toBe('SPAN')
    expect(el.attributes('aria-disabled')).toBe('true')
    expect(el.attributes('href')).toBeUndefined()
    expect(el.get('[data-testid="nav-reason"]').text()).toBe('Requires administrator')
    expect(wrapper.get('[data-testid="nav-security"]').attributes('href')).toBe('/security')
    expect(wrapper.findAll('nav a').map((a) => a.attributes('href'))).toEqual(['/', '/repos', '/packages', '/projects', '/security', '/settings'])
  })

  it('tells developers that instance settings are managed by administrators', () => {
    session.user = makeUser({ is_admin: false })
    const { wrapper } = mountSidebar()
    expect(wrapper.get('[data-testid="nav-settings"]').text()).toContain('Instance settings are managed by administrators')
  })

  it('treats membership of the admin group as admin', () => {
    session.user = makeUser({ is_admin: false, groups: ['admin'] })
    const { wrapper } = mountSidebar()
    expect(wrapper.find('a[href="/bundles"]').exists()).toBe(true)
  })

  it('marks the active area from the route prefix, Repositories included on artifact pages', async () => {
    session.user = makeUser({ is_admin: true })
    const { wrapper, router } = mountSidebar()
    await router.push('/repos/web-npm/packages/@acme/ui-kit')
    await flushPromises()
    const current = wrapper.findAll('nav a[aria-current="page"]')
    expect(current).toHaveLength(1)
    expect(current[0]!.attributes('href')).toBe('/repos')

    await router.push('/settings/tokens')
    await flushPromises()
    expect(wrapper.get('nav a[aria-current="page"]').attributes('href')).toBe('/settings')
  })

  it('shows no account controls: the user menu owns them', () => {
    session.user = makeUser({ username: 'testuser' })
    const { wrapper } = mountSidebar()
    expect(wrapper.text()).not.toContain('testuser')
    expect(wrapper.find('button[aria-label="Sign out"]').exists()).toBe(false)
  })

  describe('collapsible rail', () => {
    it('toggles and persists the preference', async () => {
      const { wrapper } = mountSidebar()
      await wrapper.get('button[aria-label="Collapse sidebar"]').trigger('click')
      expect(ui.sidebarCollapsed).toBe(true)
      expect(wrapper.find('button[aria-label="Expand sidebar"]').exists()).toBe(true)
      expect(JSON.parse(localStorage.getItem('oa.sidebar.collapsed') ?? 'null')).toBe(true)
    })

    it('narrows the rail and keeps accessible names and tooltips when collapsed', async () => {
      session.user = makeUser({ is_admin: false })
      ui.sidebarCollapsed = true
      const { wrapper } = mountSidebar()
      const tooltip = () => document.body.querySelector('[role="tooltip"]')
      expect(wrapper.get('#app-sidebar').classes()).toContain('md:w-16')
      // The tooltip lives outside the scrolling nav (it would widen and clip it) and only while pointed at.
      expect(tooltip()).toBeNull()
      const repos = wrapper.get('[data-testid="nav-repos"]')
      expect(repos.get('.sr-only').text()).toBe('Repositories')
      await repos.trigger('mouseenter')
      expect(tooltip()?.textContent?.trim()).toBe('Repositories')
      expect(wrapper.get('nav').find('[role="tooltip"]').exists()).toBe(false)
      await repos.trigger('mouseleave')
      expect(tooltip()).toBeNull()
      // A locked item carries its reason in the name and the tooltip, also on keyboard focus.
      const bundles = wrapper.get('[data-testid="nav-bundles"]')
      expect(bundles.get('.sr-only').text()).toContain('Requires administrator')
      await bundles.trigger('focusin')
      expect(tooltip()?.textContent).toContain('Requires administrator')
      await bundles.trigger('keydown', { key: 'Escape' })
      expect(tooltip()).toBeNull()
      wrapper.unmount()
    })

    it('uses the full width when expanded', () => {
      const { wrapper } = mountSidebar()
      expect(wrapper.get('#app-sidebar').classes()).toContain('md:w-60')
    })
  })

  describe('white-label name', () => {
    it('lets a long product name wrap instead of truncating it', async () => {
      const branding = useBrandingStore()
      const long = 'Acme Corporation Internal Artifact Hub'
      branding.set({ product_name: long, source_url: 'https://github.com/openartifact/openartifact' })
      const { wrapper } = mountSidebar()
      const name = wrapper.findAll('span').filter((s) => s.text() === long).at(-1)!
      expect(name.exists()).toBe(true)
      expect(name.classes()).not.toContain('truncate')
      expect(name.classes()).toContain('[overflow-wrap:anywhere]')
    })
  })
})
