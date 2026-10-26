// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  RECENT_LIMIT,
  loadRecent,
  pushRecent,
  recentFromPath,
  recentStorageKey,
  saveRecent,
  type RecentItem,
} from '../paletteRecent'

const repo = (name: string): RecentItem => ({ kind: 'repo', to: `/repos/${name}`, label: name })

describe('pushRecent', () => {
  it('puts the newest first and dedupes by path', () => {
    const list = pushRecent(pushRecent(pushRecent([], repo('a')), repo('b')), repo('a'))
    expect(list.map((r) => r.to)).toEqual(['/repos/a', '/repos/b'])
  })

  it('stays bounded', () => {
    let list: RecentItem[] = []
    for (let i = 0; i < RECENT_LIMIT + 5; i++) list = pushRecent(list, repo(`r${i}`))
    expect(list).toHaveLength(RECENT_LIMIT)
    expect(list[0]!.to).toBe(`/repos/r${RECENT_LIMIT + 4}`)
  })
})

describe('storage', () => {
  const memory = () => {
    const data = new Map<string, string>()
    return { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) }
  }

  it('round-trips a list', () => {
    const s = memory()
    saveRecent(s, 'k', [repo('a')])
    expect(loadRecent(s, 'k')).toEqual([repo('a')])
  })

  it('keeps one list per user', () => {
    expect(recentStorageKey('u1')).not.toBe(recentStorageKey('u2'))
  })

  it('drops malformed and off-site entries and survives broken storage', () => {
    const s = memory()
    s.setItem('k', JSON.stringify([repo('a'), { kind: 'repo', to: 'https://evil.example', label: 'x' }, { kind: 'repo', to: '//evil.example', label: 'x' }, { kind: 'nope', to: '/x', label: 'x' }, 7]))
    expect(loadRecent(s, 'k').map((r) => r.to)).toEqual(['/repos/a'])
    s.setItem('k', '{not json')
    expect(loadRecent(s, 'k')).toEqual([])
    expect(loadRecent(undefined, 'k')).toEqual([])
    expect(() => saveRecent({ setItem: () => { throw new Error('full') } }, 'k', [])).not.toThrow()
  })
})

describe('recentFromPath', () => {
  it('remembers area pages and settings sections, but not Home', () => {
    expect(recentFromPath('/repos')).toMatchObject({ kind: 'page', label: 'Repositories' })
    expect(recentFromPath('/settings/webhooks')).toMatchObject({ kind: 'section', label: 'Webhooks', sub: 'Integrations' })
    expect(recentFromPath('/')).toBeNull()
    expect(recentFromPath('/settings/nope')).toBeNull()
  })

  it('remembers repositories, artifacts, projects and bundles by path', () => {
    expect(recentFromPath('/repos/web-npm')).toEqual({ kind: 'repo', to: '/repos/web-npm', label: 'web-npm' })
    expect(recentFromPath('/repos/web-npm/packages/@acme/ui-kit')).toEqual({
      kind: 'package',
      to: '/repos/web-npm/packages/@acme/ui-kit',
      label: '@acme/ui-kit',
      sub: 'web-npm',
    })
    expect(recentFromPath('/repos/web-npm/packages/lodash/versions/4.17.21')).toMatchObject({ label: 'lodash 4.17.21', sub: 'web-npm' })
    expect(recentFromPath('/projects/storefront')).toMatchObject({ kind: 'project', label: 'storefront' })
    expect(recentFromPath('/bundles/shop/2.5.0')).toMatchObject({ kind: 'bundle', label: 'shop 2.5.0' })
  })

  it('ignores pages that are not worth returning to', () => {
    expect(recentFromPath('/login')).toBeNull()
    expect(recentFromPath('/some/where/else')).toBeNull()
  })
})
