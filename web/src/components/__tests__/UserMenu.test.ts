// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn, type Mock } from 'bun:test'
import { DOMWrapper, mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { createPinia, setActivePinia, type StoreGeneric } from 'pinia'
import * as authApi from '../../api/auth'
import { ApiError } from '../../api/client'
import { useSessionStore, type User } from '../../stores/session'
import { useThemeStore } from '../../stores/theme'
import { useToastStore } from '../../stores/toast'
import { useUIStore } from '../../stores/ui'
import UserMenu from '../UserMenu.vue'

// The panel is teleported to the body, outside the wrapper.
const body = () => new DOMWrapper(document.body)

let session: StoreGeneric
let toasts: StoreGeneric
let logout: Mock<typeof authApi.logout>

const makeUser = (overrides: Record<string, unknown> = {}): User =>
  ({ id: 'u1', username: 'testuser', is_admin: false, groups: [], ...overrides }) as User

async function mountMenu() {
  const page = { template: '<div/>' }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: ['/', '/login', '/repos', '/settings/:section'].map((path) => ({ path, component: page })),
  })
  await router.push('/repos')
  const wrapper = mount(UserMenu, { attachTo: document.body, global: { plugins: [router] } })
  await wrapper.get('[data-testid="user-menu"]').trigger('click')
  await flushPromises()
  return { wrapper, router }
}

describe('UserMenu', () => {
  beforeEach(() => {
    document.body.replaceChildren()
    setActivePinia(createPinia())
    session = useSessionStore()
    toasts = useToastStore()
    spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ version: '9.9.9' }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )
    logout = spyOn(authApi, 'logout').mockResolvedValue(undefined)
  })

  it('names the user and the role', async () => {
    session.user = makeUser({ username: 'chen_wei', is_admin: true })
    const { wrapper } = await mountMenu()
    expect(wrapper.get('[data-testid="user-menu"]').attributes('aria-label')).toBe('Account menu for chen_wei')
    expect(body().get('[data-testid="user-menu-name"]').text()).toBe('chen_wei')
    expect(body().get('[data-testid="user-menu-role"]').text()).toBe('Administrator')
  })

  it('shows a non-admin as a member and "?" without a user', async () => {
    session.user = makeUser()
    expect((await mountMenu(), body()).get('[data-testid="user-menu-role"]').text()).toBe('Member')
    session.user = null
    expect((await mountMenu()).wrapper.get('[data-testid="user-menu"] [data-initial]').attributes('data-initial')).toBe('?')
  })

  it('links the profile and the personal access tokens, and closes on navigation', async () => {
    session.user = makeUser()
    const { router } = await mountMenu()
    expect(body().find('a[href="/settings/profile"]').exists()).toBe(true)
    await body().get('a[href="/settings/tokens?type=personal"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe('/settings/tokens?type=personal')
    expect(body().find('[data-testid="user-menu-panel"]').exists()).toBe(false)
  })

  it('is a disclosure button: collapsed until pressed, then expanded and controlling a fixed panel on the body', async () => {
    session.user = makeUser()
    const { wrapper } = await mountMenu()
    const button = wrapper.get('[data-testid="user-menu"]')
    const panel = body().get('[data-testid="user-menu-panel"]')
    expect(button.attributes('aria-expanded')).toBe('true')
    expect(button.attributes('aria-controls')).toBe(panel.attributes('id'))
    expect(wrapper.element.contains(panel.element)).toBe(false)
    expect((panel.element as HTMLElement).style.position).toBe('fixed')
  })

  it('closes on Escape with the focus back on the menu button', async () => {
    session.user = makeUser()
    const { wrapper } = await mountMenu()
    ;(body().get('[data-testid="about-open"]').element as HTMLElement).focus()
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(body().find('[data-testid="user-menu-panel"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="user-menu"]').attributes('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(wrapper.get('[data-testid="user-menu"]').element)
  })

  it('closes on a press outside', async () => {
    session.user = makeUser()
    await mountMenu()
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(body().find('[data-testid="user-menu-panel"]').exists()).toBe(false)
  })

  it('switches the theme between light, dark and system', async () => {
    session.user = makeUser()
    const theme = useThemeStore()
    const { wrapper } = await mountMenu()
    const group = body().get('[role="radiogroup"]')
    expect(group.attributes('aria-label')).toBe('Theme')
    await body().get('[data-testid="theme-dark"]').trigger('click')
    expect(theme.mode).toBe('dark')
    expect(body().get('[data-testid="theme-dark"]').attributes('aria-checked')).toBe('true')
    expect(body().get('[data-testid="theme-light"]').attributes('aria-checked')).toBe('false')
    await body().get('[data-testid="theme-auto"]').trigger('click')
    expect(theme.mode).toBe('auto')
  })

  it('opens About with the attribution', async () => {
    session.user = makeUser()
    const { wrapper } = await mountMenu()
    await body().get('[data-testid="about-open"]').trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('Powered by OpenArtifact')
  })

  it('signs out, clears the session and goes to /login', async () => {
    session.user = makeUser()
    const { wrapper, router } = await mountMenu()
    await body().get('[data-testid="sign-out"]').trigger('click')
    await flushPromises()
    expect(logout).toHaveBeenCalled()
    expect(session.user).toBeNull()
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('stays signed in and reports the failure when the server cannot be reached', async () => {
    logout.mockRejectedValueOnce(new Error('network fail'))
    session.user = makeUser()
    const { wrapper, router } = await mountMenu()
    await body().get('[data-testid="sign-out"]').trigger('click')
    await flushPromises()
    expect(session.user).not.toBeNull()
    expect(router.currentRoute.value.path).toBe('/repos')
    expect(toasts.queue.map((x: { title: string }) => x.title)).toContain('Could not sign out')
  })

  it('signs out locally when the server says the session is already gone', async () => {
    logout.mockRejectedValueOnce(new ApiError('Unauthorized', 401))
    session.user = makeUser()
    const { wrapper, router } = await mountMenu()
    await body().get('[data-testid="sign-out"]').trigger('click')
    await flushPromises()
    expect(session.user).toBeNull()
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('stays reachable and working with the sidebar collapsed', async () => {
    useUIStore().sidebarCollapsed = true
    session.user = makeUser()
    const { wrapper, router } = await mountMenu()
    await body().get('[data-testid="sign-out"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/login')
  })
})
