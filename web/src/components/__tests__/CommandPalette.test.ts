// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import { createPinia, setActivePinia, type StoreGeneric } from 'pinia'
import * as searchApi from '../../api/search'
import * as paletteApi from '../../api/paletteSearch'
import * as projectsApi from '../../api/projects'
import * as authApi from '../../api/auth'
import { setLocale } from '../../i18n'
import { useLanguage } from '../../composables/useLanguage'
import { loadRecent, recentStorageKey } from '../../lib/paletteRecent'
import { useSessionStore, type User } from '../../stores/session'
import { useThemeStore } from '../../stores/theme'
import { useToastStore } from '../../stores/toast'
import { useUIStore } from '../../stores/ui'
import CommandPalette from '../CommandPalette.vue'

let session: StoreGeneric
let ui: StoreGeneric

interface Who {
  isAdmin?: boolean
  projects?: { name: string; role: string }[]
}

async function open(who: Who | boolean = true) {
  const { isAdmin = true, projects = [] } = typeof who === 'boolean' ? { isAdmin: who } : who
  session.user = { id: 'u1', username: 'tester', is_admin: isAdmin, groups: [], projects } as unknown as User
  const page = { template: '<div/>' }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: ['/', '/login', '/repos', '/repos/:repo', '/packages', '/projects', '/bundles', '/settings/:section'].map((path) => ({
      path,
      component: page,
    })),
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
const titlesShown = () => [...document.querySelectorAll('[role="option"]')].map((o) => o.textContent ?? '')
const activeTitle = () => document.getElementById(input().getAttribute('aria-activedescendant') ?? '')?.textContent ?? ''
const repo = (name: string, extra: Record<string, unknown> = {}) => ({ id: name, name, format: 'npm', repo_type: 'hosted', ...extra })
/** Types a query and waits for the real signal that its search ran: the status leaves "Searching…". */
const typeAndSearch = async (v: string) => {
  await type(v)
  const status = () => document.querySelector('[data-testid="palette-status"]')!.textContent
  for (let i = 0; i < 100 && status() === 'Searching…'; i++) {
    await new Promise((r) => setTimeout(r, 20))
    await flushPromises()
  }
  expect(status()).not.toBe('Searching…')
}
const press = async (key: string) => {
  input().dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
  await flushPromises()
}

describe('CommandPalette', () => {
  afterEach(async () => {
    await useLanguage().choose('auto')
    await setLocale('en')
  })

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

  describe('actions', () => {
    let writeText: ReturnType<typeof spyOn>

    beforeEach(() => {
      // The clipboard is the boundary: the real one does not exist in the test DOM.
      const clipboard = { writeText: async (_: string) => {} }
      Object.defineProperty(navigator, 'clipboard', { value: clipboard, configurable: true })
      writeText = spyOn(clipboard, 'writeText').mockResolvedValue(undefined)
    })

    afterEach(() => {
      Reflect.deleteProperty(navigator, 'clipboard')
    })

    it('runs an action from the keyboard alone: type, arrow, Enter', async () => {
      const { router } = await open()
      await type('>create')
      expect(activeTitle()).toContain('Create repository')
      await press('ArrowDown')
      expect(activeTitle()).toContain('Create project')
      await press('ArrowDown')
      await press('Enter')
      expect(router.currentRoute.value.fullPath).toBe('/settings/tokens?new=1')
      expect(ui.commandPaletteOpen).toBe(false)
    })

    it('hides the actions the user may not run', async () => {
      await open(false)
      await type('>create')
      expect(titlesShown().map((x) => x.replace(/Action$/, ''))).toEqual(['Create access token'])
      await type('>')
      const all = titlesShown().join('|')
      expect(all).not.toContain('Create user')
      expect(all).not.toContain('Create release bundle')
      expect(all).not.toContain('Create repository')
    })

    it('offers repository creation to a project maintainer and project creation when the API allows it', async () => {
      spyOn(projectsApi, 'listProjects').mockResolvedValue({ projects: [], can_create: true })
      await open({ isAdmin: false, projects: [{ name: 'web', role: 'maintainer' }] })
      await type('>create')
      const shown = titlesShown().join('|')
      expect(shown).toContain('Create repository')
      expect(shown).toContain('Create project')
      expect(shown).not.toContain('Create user')
    })

    it('opens the settings of a repository whose name matches, for an administrator', async () => {
      spyOn(paletteApi, 'searchRepositories').mockResolvedValue([repo('web-npm') as never])
      const { router } = await open()
      await typeAndSearch('web-npm')
      expect(titlesShown().some((x) => x.includes('Open settings of web-npm'))).toBe(true)
      document.querySelector<HTMLElement>('[data-testid^="palette-option-action:repo-settings"]')!.click()
      await flushPromises()
      expect(router.currentRoute.value.fullPath).toBe('/repos/web-npm?tab=settings')
    })

    it('does not offer repository settings to a user without admin rights on that repository', async () => {
      spyOn(paletteApi, 'searchRepositories').mockResolvedValue([repo('data-raw', { project: 'data' }) as never])
      await open({ isAdmin: false, projects: [{ name: 'data', role: 'developer' }] })
      await typeAndSearch('data-raw')
      const shown = titlesShown().join('|')
      expect(shown).toContain('Copy install command for data-raw')
      expect(shown).not.toContain('Open settings of data-raw')
    })

    it('copies the install command of a repository and confirms it', async () => {
      spyOn(paletteApi, 'searchRepositories').mockResolvedValue([repo('web-npm') as never])
      await open(false)
      await typeAndSearch('>copy web-npm')
      expect(activeTitle()).toContain('Copy install command for web-npm')
      await press('Enter')
      expect(writeText).toHaveBeenCalledTimes(1)
      expect(writeText.mock.calls[0]![0]).toContain(`${window.location.origin}/repos/web-npm`)
      expect(useToastStore().queue.map((q) => q.title)).toEqual(['Install command for web-npm copied'])
      expect(ui.commandPaletteOpen).toBe(false)
    })

    it('says so instead of claiming success when the clipboard refuses', async () => {
      writeText.mockRejectedValue(new Error('denied'))
      spyOn(document, 'execCommand').mockReturnValue(false)
      spyOn(paletteApi, 'searchRepositories').mockResolvedValue([repo('web-npm') as never])
      await open()
      await typeAndSearch('>copy web-npm')
      await press('Enter')
      const toasts = useToastStore().queue
      expect(toasts.map((q) => q.level)).toEqual(['error'])
    })

    it('switches the theme and names the opposite one', async () => {
      useThemeStore().setMode('light')
      await open()
      await type('>theme')
      expect(activeTitle()).toContain('Switch to dark theme')
      await press('Enter')
      expect(useThemeStore().mode).toBe('dark')
      ui.openCommandPalette()
      await flushPromises()
      await type('>theme')
      expect(activeTitle()).toContain('Switch to light theme')
    })

    it('changes the language and no longer offers the current one', async () => {
      await open()
      await type('>italiano')
      await press('Enter')
      await flushPromises()
      expect(document.documentElement.lang).toBe('it')
      expect(useLanguage().choice.value).toBe('it')
    })

    it('signs out', async () => {
      spyOn(authApi, 'logout').mockResolvedValue(undefined)
      const { router } = await open()
      await type('>sign out')
      await press('Enter')
      await flushPromises()
      expect(session.user).toBeNull()
      expect(router.currentRoute.value.path).toBe('/login')
    })

    it('puts a run action into Recent for the next time, but not sign-out', async () => {
      const { wrapper } = await open()
      await type('>token')
      await press('Enter')
      await flushPromises()
      expect(loadRecent(window.localStorage, recentStorageKey('u1'))).toEqual([{ kind: 'action', action: 'create-token' }])
      wrapper.unmount()

      await open()
      expect(document.querySelector('[data-testid="palette-option-recent:action:create-token"]')).not.toBeNull()
    })

    it('announces the number of results and the actions found for a repository', async () => {
      spyOn(paletteApi, 'searchRepositories').mockResolvedValue([repo('web-npm') as never])
      await open()
      await typeAndSearch('>web-npm')
      expect(document.querySelector('[data-testid="palette-status"]')!.textContent).toBe('2 results')
    })
  })
})
