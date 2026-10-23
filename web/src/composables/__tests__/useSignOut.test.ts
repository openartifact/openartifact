// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn, type Mock } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h } from 'vue'
import * as authApi from '../../api/auth'
import { useSessionStore, type User } from '../../stores/session'
import { useToastStore } from '../../stores/toast'
import { useSignOut } from '../useSignOut'

let assign: Mock<(url: string | URL) => void>

async function signOut() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div/>' } },
      { path: '/login', component: { template: '<div/>' } },
    ],
  })
  await router.push('/')

  const Host = defineComponent({
    setup() {
      const run = useSignOut()
      return () => h('button', { onClick: run }, 'out')
    },
  })
  const wrapper = mount(Host, { global: { plugins: [router] } })
  await wrapper.find('button').trigger('click')
  await flushPromises()

  return router
}

describe('useSignOut', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    useSessionStore().user = { id: 'u1', username: 'alice' } as User
    // Restored by mock.restore() in the global afterEach (test/isolation.ts).
    assign = spyOn(window.location, 'assign').mockImplementation(() => {})
  })

  it('goes to /login when the server returns no IdP logout URL', async () => {
    spyOn(authApi, 'logout').mockResolvedValue(undefined)

    const router = await signOut()

    expect(useSessionStore().user).toBeNull()
    expect(router.currentRoute.value.path).toBe('/login')
    expect(assign).not.toHaveBeenCalled()
  })

  it('navigates to the IdP end-session URL, after clearing the client state', async () => {
    let userWhenNavigating: User | null | undefined
    assign.mockImplementation(() => {
      userWhenNavigating = useSessionStore().user
    })
    spyOn(authApi, 'logout').mockResolvedValue('https://idp.example/logout?id_token_hint=x')

    const router = await signOut()

    expect(assign).toHaveBeenCalledWith('https://idp.example/logout?id_token_hint=x')
    expect(userWhenNavigating).toBeNull()
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('stays signed in, shows the error and does not navigate when the logout call fails', async () => {
    spyOn(authApi, 'logout').mockRejectedValue(new Error('network fail'))

    const router = await signOut()

    expect(useSessionStore().user).not.toBeNull()
    expect(useToastStore().queue.length).toBe(1)
    expect(assign).not.toHaveBeenCalled()
    expect(router.currentRoute.value.path).toBe('/')
  })
})
