// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  EMPTY_ENTITIES,
  buildGroups,
  checksumPath,
  flattenGroups,
  highlightParts,
  itemHint,
  matchScore,
  moveActive,
  parseQuery,
  rankItems,
  type EntityResults,
  type PaletteItem,
} from '../palette'
import type { RecentItem } from '../paletteRecent'

const admin = { isAdmin: true, canCreateRepo: true }
const dev = { isAdmin: false, canCreateRepo: false }

const item = (title: string, extra: Partial<PaletteItem> = {}): PaletteItem => ({
  id: title,
  group: 'repositories',
  title,
  icon: 'repos',
  ...extra,
})

function groups(raw: string, ctx = admin, entities: EntityResults = EMPTY_ENTITIES, recent: RecentItem[] = []) {
  return buildGroups({ query: parseQuery(raw), context: ctx, recent, entities })
}
const ids = (raw: string, ctx = admin, entities?: EntityResults, recent?: RecentItem[]) =>
  groups(raw, ctx, entities, recent).map((g) => g.id)
const titles = (raw: string, group: string, ctx = admin) =>
  groups(raw, ctx).find((g) => g.id === group)?.items.map((i) => i.title) ?? []

describe('parseQuery', () => {
  it('trims and has no prefix by default', () => {
    expect(parseQuery('  repos ')).toEqual({ mode: 'all', text: 'repos', checksum: null })
  })

  it('reads ">" as actions only, with or without a space', () => {
    expect(parseQuery('>create')).toMatchObject({ mode: 'actions', text: 'create' })
    expect(parseQuery('> create repo')).toMatchObject({ mode: 'actions', text: 'create repo' })
    expect(parseQuery('>')).toMatchObject({ mode: 'actions', text: '' })
  })

  it('detects a 32 to 64 digit hex string as a checksum and lower-cases it', () => {
    const md5 = 'D41D8CD98F00B204E9800998ECF8427E'
    expect(parseQuery(md5).checksum).toBe(md5.toLowerCase())
    expect(parseQuery('a'.repeat(40)).checksum).toBe('a'.repeat(40))
    expect(parseQuery('0'.repeat(64)).checksum).not.toBeNull()
  })

  it('does not treat shorter, longer or non-hex text as a checksum', () => {
    expect(parseQuery('a'.repeat(31)).checksum).toBeNull()
    expect(parseQuery('a'.repeat(65)).checksum).toBeNull()
    expect(parseQuery('g'.repeat(40)).checksum).toBeNull()
    expect(parseQuery('>' + 'a'.repeat(40)).checksum).toBeNull()
  })
})

describe('matching and ranking', () => {
  it('scores title matches above keyword matches above description matches', () => {
    const exact = matchScore({ title: 'Webhooks' }, 'webhooks')
    const prefix = matchScore({ title: 'Webhooks' }, 'web')
    const word = matchScore({ title: 'Storage backends' }, 'back')
    const sub = matchScore({ title: 'Webhooks' }, 'hook')
    const kw = matchScore({ title: 'Quotas', keywords: 'limit usage' }, 'limit')
    const desc = matchScore({ title: 'Quotas', description: 'Size limits' }, 'limits')
    expect(exact).toBeGreaterThan(prefix)
    expect(prefix).toBeGreaterThan(word)
    expect(word).toBeGreaterThan(sub)
    expect(sub).toBeGreaterThan(kw)
    expect(kw).toBeGreaterThan(desc)
    expect(desc).toBeGreaterThan(0)
    expect(matchScore({ title: 'Quotas' }, 'zzz')).toBe(0)
  })

  it('requires every token to match', () => {
    expect(matchScore({ title: 'Create repository', keywords: 'new' }, 'create repo')).toBeGreaterThan(0)
    expect(matchScore({ title: 'Create repository' }, 'create user')).toBe(0)
  })

  it('ranks best first and keeps the given order for ties', () => {
    const ranked = rankItems([item('my-api'), item('api'), item('api-gateway'), item('other')], 'api')
    expect(ranked.map((i) => i.title)).toEqual(['api', 'api-gateway', 'my-api'])
  })

  it('splits a title around the match for highlighting', () => {
    expect(highlightParts('web-npm', 'NPM')).toEqual([{ text: 'web-', match: false }, { text: 'npm', match: true }])
    expect(highlightParts('web-npm', 'zzz')).toEqual([{ text: 'web-npm', match: false }])
    expect(highlightParts('web-npm', '')).toEqual([{ text: 'web-npm', match: false }])
  })
})

describe('buildGroups', () => {
  it('shows Recent, Pages and a few Actions for an empty query', () => {
    const recent: RecentItem[] = [{ kind: 'repo', to: '/repos/web-npm', label: 'web-npm' }]
    expect(ids('', admin, EMPTY_ENTITIES, recent)).toEqual(['recent', 'pages', 'actions'])
    expect(ids('')).toEqual(['pages', 'actions'])
    expect(groups('').find((g) => g.id === 'pages')!.items.map((i) => i.title)).toEqual([
      'Home', 'Repositories', 'Packages', 'Projects', 'Bundles', 'Security', 'Settings',
    ])
  })

  it('shows only actions after ">"', () => {
    expect(ids('>')).toEqual(['actions'])
    expect(titles('>create repo', 'actions')).toEqual(['Create repository'])
    expect(titles('>theme', 'actions')).toEqual(['Toggle theme'])
    expect(ids('>zzzz')).toEqual([])
  })

  it('offers the verbs the spec names', () => {
    expect(titles('>', 'actions')).toEqual(expect.arrayContaining(['Create repository', 'Create project', 'Create access token', 'Toggle theme', 'Sign out']))
  })

  it('finds a page, a settings section and its keywords', () => {
    expect(titles('repos', 'pages')).toEqual(['Repositories'])
    expect(titles('webhook', 'settings')).toEqual(['Webhooks'])
    const retention = groups('retention').find((g) => g.id === 'settings')!.items.map((i) => i.title)
    expect(retention).toEqual(expect.arrayContaining(['Cleanup policies', 'Trash']))
    expect(groups('retention').find((g) => g.id === 'settings')!.items[0]!.description).toContain('Matches “retention”')
  })

  it('marks locked pages and sections for a non-admin and keeps them listed', () => {
    const pages = groups('bundles', dev).find((g) => g.id === 'pages')!.items
    expect(pages).toHaveLength(1)
    expect(pages[0]!.locked).toBe(true)
    expect(itemHint(pages[0]!)).toBe('Requires admin')
    const users = groups('users', dev).find((g) => g.id === 'settings')!.items.find((i) => i.title === 'Users')!
    expect(users.locked).toBe(true)
    const profile = groups('profile', dev).find((g) => g.id === 'settings')!.items.find((i) => i.title === 'Profile')!
    expect(profile.locked).toBe(false)
  })

  it('locks the admin-only actions and repository creation for people who cannot create repositories', () => {
    const all = groups('>create', dev)[0]!.items
    const by = Object.fromEntries(all.map((i) => [i.title, i]))
    expect(by['Create repository']!.locked).toBe(true)
    expect(by['Create user']!.locked).toBe(true)
    expect(by['Create release bundle']!.locked).toBe(true)
    expect(by['Create project']!.locked).toBeFalsy()
    expect(by['Create access token']!.locked).toBeFalsy()
    const ok = groups('>create repo', { isAdmin: false, canCreateRepo: true })[0]!.items[0]!
    expect(ok.locked).toBe(false)
  })

  it('routes the verbs and the actions that are not navigation', () => {
    const all = groups('>', admin)[0]!.items
    const by = Object.fromEntries(all.map((i) => [i.title, i]))
    expect(by['Create repository']!.to).toBe('/repos?new=1')
    expect(by['Create project']!.to).toBe('/projects?new=1')
    expect(by['Create access token']!.to).toBe('/settings/tokens?new=1')
    expect(by['Toggle theme']!.action).toBe('toggle-theme')
    expect(by['Sign out']!.action).toBe('sign-out')
  })

  it('leads with a checksum lookup routed to the Packages page in checksum mode', () => {
    const hex = 'ABCDEF0123456789'.repeat(4)
    const g = groups(hex)
    expect(g[0]!.id).toBe('search')
    expect(g[0]!.items[0]!.title).toBe('Look up checksum in Packages')
    expect(g[0]!.items[0]!.to).toBe(`/packages?q=${hex.toLowerCase()}&mode=checksum`)
    expect(checksumPath('abc')).toBe('/packages?q=abc&mode=checksum')
    expect(g.flatMap((x) => x.items).some((i) => i.title.startsWith('Search all packages'))).toBe(false)
  })

  it('ends with "search all packages" for ordinary text', () => {
    const g = groups('lodash')
    const last = g[g.length - 1]!
    expect(last.id).toBe('search')
    expect(last.items[0]!.to).toBe('/packages?q=lodash')
  })

  it('orders entity groups and ranks the ones the server does not rank', () => {
    const entities: EntityResults = {
      repositories: [item('my-zeta', { id: 'repo:my-zeta' }), item('zeta', { id: 'repo:zeta' }), item('unrelated', { id: 'repo:u' })],
      packages: [item('left-pad', { id: 'p1', group: 'packages' })],
      projects: [item('zeta-team', { id: 'pr1', group: 'projects' })],
      bundles: [item('zeta 1.0.0', { id: 'b1', group: 'bundles' })],
      users: [item('zetauser', { id: 'u1', group: 'users' })],
    }
    const g = groups('zeta', admin, entities)
    expect(g.map((x) => x.id)).toEqual(['repositories', 'packages', 'projects', 'bundles', 'users', 'search'])
    expect(g[0]!.items.map((i) => i.title)).toEqual(['zeta', 'my-zeta'])
    expect(g[1]!.items.map((i) => i.title)).toEqual(['left-pad'])
  })

  it('hides recent entries a role cannot open', () => {
    const recent: RecentItem[] = [
      { kind: 'page', to: '/bundles', label: 'Bundles' },
      { kind: 'bundle', to: '/bundles/shop/2.5.0', label: 'shop 2.5.0' },
      { kind: 'section', to: '/settings/users', label: 'Users' },
      { kind: 'repo', to: '/repos/web-npm', label: 'web-npm' },
    ]
    const dev1 = groups('', dev, EMPTY_ENTITIES, recent).find((g) => g.id === 'recent')!.items.map((i) => i.title)
    expect(dev1).toEqual(['web-npm'])
    const adm = groups('', admin, EMPTY_ENTITIES, recent).find((g) => g.id === 'recent')!.items
    expect(adm).toHaveLength(4)
  })

  it('does not show Recent while a query is typed', () => {
    const recent: RecentItem[] = [{ kind: 'repo', to: '/repos/web-npm', label: 'web-npm' }]
    expect(ids('web', admin, EMPTY_ENTITIES, recent)).not.toContain('recent')
  })

  it('flattens in display order', () => {
    expect(flattenGroups(groups('')).length).toBe(groups('').reduce((n, g) => n + g.items.length, 0))
  })
})

describe('moveActive', () => {
  it('moves with the arrows and wraps', () => {
    expect(moveActive('ArrowDown', 0, 3)).toBe(1)
    expect(moveActive('ArrowDown', 2, 3)).toBe(0)
    expect(moveActive('ArrowUp', 0, 3)).toBe(2)
    expect(moveActive('ArrowUp', 2, 3)).toBe(1)
  })

  it('jumps to the ends', () => {
    expect(moveActive('Home', 2, 5)).toBe(0)
    expect(moveActive('End', 0, 5)).toBe(4)
  })

  it('ignores other keys and empty lists', () => {
    expect(moveActive('a', 0, 3)).toBeNull()
    expect(moveActive('ArrowDown', 0, 0)).toBeNull()
  })
})
