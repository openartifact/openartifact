// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import ChangePasswordPage from '../ChangePasswordPage.vue'
import { useSessionStore, type User } from '../../stores/session'
import { useToastStore } from '../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const LONG = 'correct-horse-battery'
const OLD = 'old-password-value'

interface Opts {
  changeResponse?: () => Response | Promise<Response>
  me?: () => Response | Promise<Response>
  path?: string
}

async function mountPage(opts: Opts = {}) {
  setActivePinia(createPinia())
  const session = useSessionStore()
  session.user = { id: 'u', username: 'bob', must_change_password: true } as User
  const spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: unknown) => {
    const url = String(input)
    if (url.includes('/auth/change-password')) return (opts.changeResponse ?? (() => new Response(null, { status: 204 })))()
    if (url.includes('/auth/me')) return (opts.me ?? (() => json({ id: 'u', username: 'bob' })))()
    return json({})
  }) as never)
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/change-password', name: 'change-password', component: ChangePasswordPage },
      { path: '/login', name: 'login', component: { template: '<div/>' } },
      { path: '/:rest(.*)*', component: { template: '<div/>' } },
    ],
  })
  await router.push(opts.path ?? '/change-password')
  await router.isReady()
  const w = mount(ChangePasswordPage, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return { w, router, spy, session }
}

async function fill(w: ReturnType<typeof mount>, cur: string, next: string, confirm: string) {
  await w.get('#current_password').setValue(cur)
  await w.get('#new_password').setValue(next)
  await w.get('#confirm_password').setValue(confirm)
  await w.get('form').trigger('submit')
  await flushPromises()
}

const changeCalls = (spy: ReturnType<typeof spyOn>) =>
  spy.mock.calls.filter((c) => String(c[0]).includes('/auth/change-password'))

describe('ChangePasswordPage validation', () => {
  const cases: [string, string, string, string, string][] = [
    ['too short', OLD, 'short', 'short', 'new_password'],
    ['11 characters', OLD, 'a'.repeat(11), 'a'.repeat(11), 'new_password'],
    ['confirmation mismatch', OLD, LONG, LONG + 'x', 'confirm_password'],
    ['same as current', LONG, LONG, LONG, 'new_password'],
  ]
  for (const [name, cur, next, confirm, field] of cases) {
    it(`rejects ${name} without calling the server`, async () => {
      const { w, spy } = await mountPage()
      await fill(w, cur, next, confirm)
      expect(changeCalls(spy)).toHaveLength(0)
      expect(w.findAll('[role="alert"]')).toHaveLength(1)
      expect(w.find(`#${field}-error`).exists()).toBe(true)
      w.unmount()
    })
  }

  it('accepts exactly 12 characters', async () => {
    const { w, spy } = await mountPage()
    await fill(w, OLD, 'a'.repeat(12), 'a'.repeat(12))
    expect(changeCalls(spy)).toHaveLength(1)
    w.unmount()
  })

  it('clears a previous field error on the next valid submit', async () => {
    const { w } = await mountPage()
    await fill(w, OLD, 'short', 'short')
    expect(w.find('#new_password-error').exists()).toBe(true)
    await fill(w, OLD, LONG, LONG)
    expect(w.find('#new_password-error').exists()).toBe(false)
    w.unmount()
  })
})

describe('ChangePasswordPage submit', () => {
  it('posts both passwords in the JSON body only and follows a safe next', async () => {
    const { w, router, spy } = await mountPage({ path: '/change-password?next=/repos/x' })
    await fill(w, OLD, LONG, LONG)
    const [url, init] = changeCalls(spy)[0] as [string, RequestInit]
    expect(url).not.toContain(OLD)
    expect(url).not.toContain(LONG)
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({ current_password: OLD, new_password: LONG })
    expect(router.currentRoute.value.fullPath).toBe('/repos/x')
    expect(router.currentRoute.value.fullPath).not.toContain(LONG)
    expect(JSON.stringify({ ...localStorage })).not.toContain(LONG)
    w.unmount()
  })

  it('falls back to / for an off-site next', async () => {
    const { w, router } = await mountPage({ path: '/change-password?next=//evil.example' })
    await fill(w, OLD, LONG, LONG)
    expect(router.currentRoute.value.fullPath).toBe('/')
    w.unmount()
  })

  it('shows the server message for a wrong current password, stays put, keeps secrets out of the text', async () => {
    const { w, router } = await mountPage({
      changeResponse: () => json({ error: 'current password is incorrect' }, 401),
    })
    await fill(w, OLD, LONG, LONG)
    expect(w.get('[role="alert"]').text()).toBe('current password is incorrect')
    expect(w.text()).not.toContain(LONG)
    expect(w.text()).not.toContain(OLD)
    expect(router.currentRoute.value.path).toBe('/change-password')
    expect(JSON.stringify({ ...localStorage })).not.toContain(LONG)
    w.unmount()
  })

  it('shows a server policy rejection', async () => {
    const { w } = await mountPage({
      changeResponse: () => json({ error: 'password is too common' }, 400),
    })
    await fill(w, OLD, LONG, LONG)
    expect(w.get('[role="alert"]').text()).toBe('password is too common')
    w.unmount()
  })

  it('shows an error and re-enables submit on a network failure', async () => {
    const { w } = await mountPage({
      changeResponse: () => Promise.reject(new TypeError('Failed to fetch')),
    })
    await fill(w, OLD, LONG, LONG)
    expect(w.get('[role="alert"]').text()).toBe('Failed to fetch')
    expect(w.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    w.unmount()
  })

  it('disables submit while in flight so a double submit sends one request', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const { w, spy } = await mountPage({
      changeResponse: async () => {
        await gate
        return new Response(null, { status: 204 })
      },
    })
    await w.get('#current_password').setValue(OLD)
    await w.get('#new_password').setValue(LONG)
    await w.get('#confirm_password').setValue(LONG)
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(w.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    release()
    await flushPromises()
    expect(changeCalls(spy)).toHaveLength(1)
    w.unmount()
  })

  it('signs out and returns to login when the user is still locked after the change', async () => {
    const { w, router, session } = await mountPage({
      me: () => json({ error: 'password_change_required', code: 'password_change_required' }, 403),
    })
    await fill(w, OLD, LONG, LONG)
    expect(session.isAuthenticated).toBe(false)
    expect(router.currentRoute.value.name).toBe('login')
    expect(useToastStore().queue.some((x) => x.level === 'warning')).toBe(true)
    w.unmount()
  })

  it('returns to login when the session is gone after the change', async () => {
    const { w, router, session } = await mountPage({ me: () => json({ error: 'unauthorized' }, 401) })
    await fill(w, OLD, LONG, LONG)
    expect(session.isAuthenticated).toBe(false)
    expect(router.currentRoute.value.name).toBe('login')
    w.unmount()
  })

  it('sign out button ends the session and goes to login', async () => {
    const { w, router, spy } = await mountPage()
    const buttons = w.findAll('button')
    await buttons[buttons.length - 1]!.trigger('click')
    await flushPromises()
    expect(spy.mock.calls.some((c) => String(c[0]).includes('/auth/logout'))).toBe(true)
    expect(router.currentRoute.value.path).toBe('/login')
    w.unmount()
  })
})
