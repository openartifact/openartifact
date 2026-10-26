// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { createPinia, setActivePinia, type StoreGeneric } from 'pinia'
import * as searchApi from '../../api/search'
import * as paletteApi from '../../api/paletteSearch'
import * as projectsApi from '../../api/projects'
import { useSessionStore, type User } from '../../stores/session'
import { useUIStore } from '../../stores/ui'
import CommandPalette from '../CommandPalette.vue'

let session: StoreGeneric
let ui: StoreGeneric

async function open(isAdmin = true) {
  session.user = { id: 'u1', username: 'tester', is_admin: isAdmin, groups: [] } as unknown as User
  const page = { template: '<div/>' }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: ['/', '/repos', '/packages', '/bundles', '/settings/:section'].map((path) => ({ path, component: page })),
  })
  await router.push('/')
  ui.openCommandPalette()
  const wrapper = mount(CommandPalette, { attachTo: document.body, global: { plugins: [router] } })
  await flushPromises()
  return { wrapper, router }
}
const input = () => document.querySelector<HTMLInputElement>('[data-testid="palette-input"]')!
const type = async (v: string) => {
  input().value = v
  input().dispatchEvent(new Event('input', { bubbles: true }))
  await flushPromises()
}
const press = async (key: string) => {
  input().dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
  await flushPromises()
}

describe('CommandPalette', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    session = useSessionStore()
    ui = useUIStore()
    spyOn(paletteApi, 'searchRepositories').mockResolvedValue([])
    spyOn(paletteApi, 'searchUsers').mockResolvedValue([])
    spyOn(searchApi, 'search').mockResolvedValue({ packages: [], repositories: [] })
    spyOn(projectsApi, 'listProjects').mockResolvedValue({ projects: [], can_create: false })
  })

  it('is a combobox over a listbox that follows the active option', async () => {
    await open()
    expect(input().getAttribute('role')).toBe('combobox')
    expect(document.querySelector('[role="listbox"]')).not.toBeNull()
    const first = document.querySelector('[role="option"]')!
    expect(input().getAttribute('aria-activedescendant')).toBe(first.id)
    await press('ArrowDown')
    const second = document.querySelectorAll('[role="option"]')[1]!
    expect(input().getAttribute('aria-activedescendant')).toBe(second.id)
    await press('End')
    const all = document.querySelectorAll('[role="option"]')
    expect(input().getAttribute('aria-activedescendant')).toBe(all[all.length - 1]!.id)
    await press('Home')
    expect(input().getAttribute('aria-activedescendant')).toBe(first.id)
  })

  it('announces the number of results', async () => {
    await open()
    await type('webhook')
    await new Promise((r) => setTimeout(r, 400))
    await flushPromises()
    expect(document.querySelector('[data-testid="palette-status"]')!.textContent).toMatch(/\d+ results?/)
  })

  it('navigates to the active result on Enter and closes', async () => {
    const { router } = await open()
    await type('repos')
    await press('Enter')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/repos')
    expect(ui.commandPaletteOpen).toBe(false)
  })

  it('shows only actions after ">"', async () => {
    await open()
    await type('>')
    const titles = [...document.querySelectorAll('[role="option"]')].map((o) => o.textContent)
    expect(titles.some((x) => x?.includes('Sign out'))).toBe(true)
    expect(titles.some((x) => x?.includes('Home'))).toBe(false)
  })

  it('looks a checksum up in Packages', async () => {
    const { router } = await open()
    const hex = 'ab12'.repeat(10)
    await type(hex)
    await press('Enter')
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe(`/packages?q=${hex}&mode=checksum`)
  })

  it('does not navigate to a locked result', async () => {
    const { router } = await open(false)
    await type('bundles')
    await press('Enter')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/')
    expect(ui.commandPaletteOpen).toBe(true)
  })
})
