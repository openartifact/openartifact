// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  actionRecentKey,
  bindRecentAction,
  globalActions,
  isRepoSubject,
  languageActions,
  repoActions,
  repoSearchTerm,
  toActionRecent,
  type ActionEnv,
  type BoundAction,
  type PaletteContext,
  type RepoSubject,
} from '../paletteActions'

const dev: PaletteContext = {
  isAdmin: false,
  canCreateRepo: false,
  canCreateProject: false,
  projectRoles: {},
  theme: 'light',
  language: 'en',
}
const admin: PaletteContext = { ...dev, isAdmin: true, canCreateRepo: true, canCreateProject: true }

const npm: RepoSubject = { name: 'web-npm', format: 'npm', repoType: 'hosted', project: 'web' }

interface Calls {
  go: string[]
  copied: string[]
  themeToggles: number
  languages: string[]
  signOuts: number
  success: string[]
  error: string[]
}

function fakeEnv(copyResult = true): { env: ActionEnv; calls: Calls } {
  const calls: Calls = { go: [], copied: [], themeToggles: 0, languages: [], signOuts: 0, success: [], error: [] }
  const env: ActionEnv = {
    origin: 'https://oa.example.com',
    go: (path) => void calls.go.push(path),
    copy: async (text) => {
      calls.copied.push(text)
      return copyResult
    },
    toggleTheme: () => void calls.themeToggles++,
    setLanguage: async (choice) => {
      calls.languages.push(choice)
      return true
    },
    signOut: async () => void calls.signOuts++,
    toast: { success: (title) => void calls.success.push(title), error: (title) => void calls.error.push(title) },
  }
  return { env, calls }
}

const byId = (list: BoundAction[], id: string) => list.find((a) => a.id === id)

describe('permission predicates', () => {
  it('offers creating a token to everybody and the rest by role', () => {
    expect(globalActions(dev).map((a) => a.id)).toEqual(['create-token', 'switch-theme', 'sign-out'])
    expect(globalActions({ ...dev, canCreateRepo: true }).map((a) => a.id)).toContain('create-repo')
    expect(globalActions({ ...dev, canCreateProject: true }).map((a) => a.id)).toContain('create-project')
    expect(globalActions(admin).map((a) => a.id)).toEqual([
      'create-repo', 'create-project', 'create-token', 'create-user', 'create-bundle', 'switch-theme', 'sign-out',
    ])
  })

  it('lets only administrators and project maintainers open the settings of a repository', () => {
    const ids = (ctx: PaletteContext, repo = npm) => repoActions(ctx, repo).map((a) => a.id)
    expect(ids(admin)).toEqual(['repo-settings', 'repo-copy-install'])
    expect(ids(dev)).toEqual(['repo-copy-install'])
    expect(ids({ ...dev, projectRoles: { web: 'viewer' } })).toEqual(['repo-copy-install'])
    expect(ids({ ...dev, projectRoles: { web: 'developer' } })).toEqual(['repo-copy-install'])
    expect(ids({ ...dev, projectRoles: { web: 'maintainer' } })).toEqual(['repo-settings', 'repo-copy-install'])
    expect(ids({ ...dev, projectRoles: { web: 'owner' } })).toEqual(['repo-settings', 'repo-copy-install'])
    // a role in another project, or a repository without a project, grants nothing
    expect(ids({ ...dev, projectRoles: { other: 'owner' } })).toEqual(['repo-copy-install'])
    expect(ids({ ...dev, projectRoles: { web: 'owner' } }, { ...npm, project: undefined })).toEqual(['repo-copy-install'])
    // the system project never grants anything, whatever the caller's role map says
    expect(ids({ ...dev, projectRoles: { system: 'owner' } }, { ...npm, project: 'system' })).toEqual(['repo-copy-install'])
  })

  it('offers every language but the one that is already chosen', () => {
    const tags = (ctx: PaletteContext) => languageActions(ctx).map((a) => a.key.replace('change-language:', ''))
    expect(tags(dev)).toEqual(['auto', 'it', 'de', 'fr', 'es', 'pt-BR', 'zh-Hans', 'ja'])
    expect(tags({ ...dev, language: 'it' })).toEqual(['auto', 'en', 'de', 'fr', 'es', 'pt-BR', 'zh-Hans', 'ja'])
    expect(tags({ ...dev, language: 'auto' })).not.toContain('auto')
  })
})

describe('running actions', () => {
  it('navigates for the create actions', async () => {
    const { env, calls } = fakeEnv()
    for (const a of globalActions(admin)) if (a.id.startsWith('create-')) await a.run(env)
    expect(calls.go).toEqual(['/repos?new=1', '/projects?new=1', '/settings/tokens?new=1', '/settings/users?new=1', '/bundles?new=1'])
  })

  it('switches the theme, the language and signs out', async () => {
    const { env, calls } = fakeEnv()
    await byId(globalActions(dev), 'switch-theme')!.run(env)
    await byId(globalActions(dev), 'sign-out')!.run(env)
    await languageActions(dev).find((a) => a.key.endsWith(':it'))!.run(env)
    expect(calls.themeToggles).toBe(1)
    expect(calls.signOuts).toBe(1)
    expect(calls.languages).toEqual(['it'])
  })

  it('opens the settings tab of the repository, encoding its name', async () => {
    const { env, calls } = fakeEnv()
    await byId(repoActions(admin, { ...npm, name: 'a b' }), 'repo-settings')!.run(env)
    expect(calls.go).toEqual(['/repos/a%20b?tab=settings'])
  })

  it('copies the first client snippet of the repository for the real origin and says so', async () => {
    const { env, calls } = fakeEnv()
    const copy = byId(repoActions(dev, npm), 'repo-copy-install')!
    await copy.run(env)
    expect(calls.copied).toHaveLength(1)
    expect(calls.copied[0]).toContain('https://oa.example.com/repos/web-npm')
    expect(calls.success).toEqual(['Install command for web-npm copied'])
    expect(calls.error).toEqual([])
  })

  it('renders the snippet of the repository type and format, with a placeholder for credentials', async () => {
    const { env, calls } = fakeEnv()
    await byId(repoActions(dev, { name: 'hub', format: 'docker', repoType: 'proxy' }), 'repo-copy-install')!.run(env)
    expect(calls.copied[0]).toContain('oa.example.com')
    expect(calls.copied[0]).not.toContain('undefined')
  })

  it('reports a failed copy instead of claiming success', async () => {
    const { env, calls } = fakeEnv(false)
    await byId(repoActions(dev, npm), 'repo-copy-install')!.run(env)
    expect(calls.success).toEqual([])
    expect(calls.error).toEqual(['Copy failed'])
  })

  it('names the client the copied snippet is for', () => {
    expect(byId(repoActions(dev, npm), 'repo-copy-install')!.description).toBe('npm')
  })
})

describe('recent runs', () => {
  it('remembers what is worth repeating and not sign-out or a language switch', () => {
    expect(toActionRecent(byId(globalActions(admin), 'create-token')!)).toEqual({ kind: 'action', action: 'create-token' })
    expect(toActionRecent(byId(globalActions(admin), 'sign-out')!)).toBeNull()
    expect(toActionRecent(languageActions(dev)[1]!)).toBeNull()
    const settings = byId(repoActions(admin, npm), 'repo-settings')!
    expect(toActionRecent(settings)).toEqual({ kind: 'action', action: 'repo-settings', repo: npm })
  })

  it('keys a repository action by the repository so two repositories are two entries', () => {
    const a = { kind: 'action', action: 'repo-settings', repo: npm } as const
    const b = { kind: 'action', action: 'repo-settings', repo: { ...npm, name: 'other' } } as const
    expect(actionRecentKey(a)).not.toBe(actionRecentKey(b))
    expect(actionRecentKey({ kind: 'action', action: 'create-token' })).toBe('action:create-token:')
  })

  it('binds a remembered run again and drops it when it is gone or no longer allowed', () => {
    expect(bindRecentAction({ kind: 'action', action: 'create-user' }, admin)?.title).toBe('Create user')
    expect(bindRecentAction({ kind: 'action', action: 'create-user' }, dev)).toBeNull()
    expect(bindRecentAction({ kind: 'action', action: 'removed' }, admin)).toBeNull()
    expect(bindRecentAction({ kind: 'action', action: 'repo-settings', repo: npm }, dev)).toBeNull()
    expect(bindRecentAction({ kind: 'action', action: 'repo-settings', repo: npm }, admin)?.title).toBe('Open settings of web-npm')
    // a global id cannot be run on a repository, nor a repository id without one
    expect(bindRecentAction({ kind: 'action', action: 'create-token', repo: npm }, admin)).toBeNull()
    expect(bindRecentAction({ kind: 'action', action: 'repo-settings' }, admin)).toBeNull()
  })

  it('accepts only well-formed stored repositories', () => {
    expect(isRepoSubject(npm)).toBe(true)
    expect(isRepoSubject({ name: 'x', format: 'npm', repoType: 'hosted' })).toBe(true)
    expect(isRepoSubject({ name: '', format: 'npm', repoType: 'hosted' })).toBe(false)
    expect(isRepoSubject({ name: 'x', format: 1, repoType: 'hosted' })).toBe(false)
    expect(isRepoSubject(null)).toBe(false)
  })
})

describe('repoSearchTerm', () => {
  it('drops the words of repository actions and keeps the name', () => {
    expect(repoSearchTerm('settings web', admin)).toBe('web')
    expect(repoSearchTerm('copy install command web-npm', admin)).toBe('web-npm')
    expect(repoSearchTerm('web-npm', admin)).toBe('web-npm')
  })

  it('keeps text made of those words only: a repository may be called "settings"', () => {
    expect(repoSearchTerm('settings', admin)).toBe('settings')
    expect(repoSearchTerm('copy install', admin)).toBe('copy install')
  })

  it('does not strip a word that merely starts like an action word', () => {
    expect(repoSearchTerm('settings copy-tools', admin)).toBe('copy-tools')
    expect(repoSearchTerm('settings set', admin)).toBe('set')
  })
})
