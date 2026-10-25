// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { SETTINGS_SECTIONS } from '../settingsSections'
import { findRange, groupOf, hubGroups, lockedMatchCount, openableSections, searchSettings, siblingRail } from '../settingsSearch'

const ids = (q: string, pool = SETTINGS_SECTIONS) => searchSettings(q, pool).map((m) => m.section.id)

describe('findRange', () => {
  it('matches word prefixes, not mid-word substrings, unless loose', () => {
    expect(findRange('Single sign-on', 'sign', false)).toEqual([7, 11])
    expect(findRange('Storage backends', 'age', false)).toBeNull()
    expect(findRange('Storage backends', 'age', true)).toEqual([4, 7])
  })
  it('escapes regular expression characters', () => {
    expect(findRange('a (b)', '(b', false)).toEqual([2, 4])
  })
})

describe('searchSettings', () => {
  it('returns nothing for an empty query', () => {
    expect(searchSettings('  ')).toEqual([])
  })

  it('ranks title matches before keyword and description matches', () => {
    const res = searchSettings('audit')
    expect(res[0]!.section.id).toBe('audit')
    expect(res[0]!.rank).toBe(0)
  })

  it('finds a section by its keywords', () => {
    const res = searchSettings('s3 bucket')
    expect(res.map((m) => m.section.id)).toContain('backends')
    expect(res.find((m) => m.section.id === 'backends')!.fields).toContain('keyword')
  })

  it('finds a section by its description', () => {
    expect(ids('analyse')).toContain('ai')
  })

  it('matches a multi-word query spread over fields', () => {
    const res = searchSettings('retention trash')
    expect(res.map((m) => m.section.id)).toContain('trash')
  })

  it('falls back to substrings only when no word prefix matches anywhere', () => {
    expect(searchSettings('ivacy')).toEqual([])
    const sub = searchSettings('ssw') // inside "password"
    expect(sub.some((m) => m.section.id === 'password' && m.loose)).toBe(true)
  })

  it('keeps registry order for equal ranks', () => {
    const res = searchSettings('token')
    const positions = res.filter((m) => m.rank === 0).map((m) => SETTINGS_SECTIONS.findIndex((s) => s.id === m.section.id))
    expect(positions).toEqual([...positions].sort((a, b) => a - b))
  })

  it('only searches the given pool', () => {
    expect(ids('users', openableSections(false))).toEqual([])
    expect(ids('users', openableSections(true))).toContain('users')
  })

  it('records the title range for highlighting', () => {
    const m = searchSettings('web')[0]!
    expect(m.section.id).toBe('webhooks')
    expect(m.titleRange).toEqual([0, 3])
  })
})

describe('role gating', () => {
  it('lets administrators open every section in seven groups', () => {
    const groups = hubGroups(true)
    expect(groups.map((g) => g.id)).toEqual(['account', 'access', 'storage', 'security', 'integrations', 'appearance', 'system'])
    expect(groups.every((g) => g.locked.length === 0)).toBe(true)
    expect(groups.flatMap((g) => g.available)).toHaveLength(SETTINGS_SECTIONS.length)
  })

  it('gives everybody else the account sections and every admin section as locked', () => {
    const groups = hubGroups(false)
    const account = groups.find((g) => g.id === 'account')!
    expect(account.available.map((s) => s.id)).toEqual(['profile', 'password', 'tokens'])
    expect(groups.filter((g) => g.id !== 'account').every((g) => g.available.length === 0)).toBe(true)
    const locked = groups.flatMap((g) => g.locked).map((s) => s.id)
    expect(locked).toContain('users')
    expect(locked).not.toContain('tokens')
    expect(locked).toHaveLength(SETTINGS_SECTIONS.filter((s) => s.adminOnly).length)
  })

  it('counts the matches a non-admin cannot open', () => {
    expect(lockedMatchCount('sso', false)).toBe(1)
    expect(lockedMatchCount('sso', true)).toBe(0)
    expect(lockedMatchCount('password', false)).toBe(1) // users: "password reset"
  })

  it('lists personal tokens under the account group for non-administrators', () => {
    const tokens = SETTINGS_SECTIONS.find((s) => s.id === 'tokens')!
    expect(groupOf(tokens, true)).toBe('access')
    expect(groupOf(tokens, false)).toBe('account')
  })
})

describe('siblingRail', () => {
  it('lists the current group first, then the others in hub order', () => {
    const rail = siblingRail('backends', true)
    expect(rail.map((g) => g.id)).toEqual(['storage', 'account', 'access', 'security', 'integrations', 'appearance', 'system'])
    expect(rail[0]!.current).toBe(true)
    expect(rail.slice(1).every((g) => !g.current)).toBe(true)
    expect(rail[0]!.sections.map((s) => s.id)).toContain('trash')
  })

  it('only lists what a non-administrator can open', () => {
    const rail = siblingRail('profile', false)
    expect(rail.map((g) => g.id)).toEqual(['account'])
    expect(rail[0]!.sections.map((s) => s.id)).toEqual(['profile', 'password', 'tokens'])
  })

  it('keeps hub order when the id is unknown', () => {
    expect(siblingRail('nope', true)[0]!.id).toBe('account')
  })
})
