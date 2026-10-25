// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { activeAreaId, resolveNav, sectionPath, settingsSections } from '../navigation'

describe('resolveNav', () => {
  it('lists the seven areas in order for every role', () => {
    const ids = ['home', 'repos', 'packages', 'projects', 'bundles', 'security', 'settings']
    expect(resolveNav(true).map((i) => i.id)).toEqual(ids)
    expect(resolveNav(false).map((i) => i.id)).toEqual(ids)
    expect(resolveNav(true).map((i) => i.to)).toEqual(['/', '/repos', '/packages', '/projects', '/bundles', '/security', '/settings'])
  })

  it('leaves every area usable for administrators', () => {
    const items = resolveNav(true)
    expect(items.every((i) => !i.locked && i.reason === null && i.note === null)).toBe(true)
  })

  it('keeps Bundles and Security visible but locked, with a reason, for non-admins', () => {
    const items = Object.fromEntries(resolveNav(false).map((i) => [i.id, i]))
    for (const id of ['bundles', 'security']) {
      expect(items[id]!.locked).toBe(true)
      expect(items[id]!.reason).toBe('Requires administrator')
    }
    for (const id of ['home', 'repos', 'packages', 'projects', 'settings']) expect(items[id]!.locked).toBe(false)
  })

  it('tells non-admins that instance settings are managed by administrators, without locking Settings', () => {
    const settings = resolveNav(false).find((i) => i.id === 'settings')!
    expect(settings.locked).toBe(false)
    expect(settings.note).toBe('Instance settings are managed by administrators')
  })
})

describe('activeAreaId', () => {
  it('follows route prefixes', () => {
    expect(activeAreaId('/')).toBe('home')
    expect(activeAreaId('/repos')).toBe('repos')
    expect(activeAreaId('/packages')).toBe('packages')
    expect(activeAreaId('/projects/storefront')).toBe('projects')
    expect(activeAreaId('/bundles/shop/2.5.0')).toBe('bundles')
    expect(activeAreaId('/security')).toBe('security')
    expect(activeAreaId('/settings/tokens')).toBe('settings')
  })

  it('highlights Repositories on repository and artifact pages', () => {
    expect(activeAreaId('/repos/web-npm')).toBe('repos')
    expect(activeAreaId('/repos/web-npm/packages/@acme/ui-kit')).toBe('repos')
    expect(activeAreaId('/repos/web-npm/packages/@acme/ui-kit/versions/4.2.0')).toBe('repos')
  })

  it('does not match a path that only shares a prefix as text, nor unknown paths', () => {
    expect(activeAreaId('/reposition')).toBeNull()
    expect(activeAreaId('/login')).toBeNull()
  })
})

describe('settings sections', () => {
  const byId = () => Object.fromEntries(settingsSections().map((s) => [s.id, s]))

  it('opens the account sections and tokens to everyone and the rest to administrators', () => {
    const s = byId()
    for (const id of ['profile', 'password', 'tokens']) expect(s[id]!.adminOnly).toBe(false)
    for (const id of ['users', 'sso', 'backends', 'scanners', 'webhooks', 'branding', 'audit', 'health', 'project-policy']) {
      expect(s[id]!.adminOnly).toBe(true)
    }
  })

  it('has a title and a group title for every section', () => {
    for (const s of settingsSections()) {
      expect(s.title.length).toBeGreaterThan(0)
      expect(s.groupTitle.length).toBeGreaterThan(0)
    }
  })

  it('sends non-admins to the personal tokens view', () => {
    expect(sectionPath('tokens', false)).toBe('/settings/tokens?type=personal')
    expect(sectionPath('tokens', true)).toBe('/settings/tokens')
    expect(sectionPath('users', true)).toBe('/settings/users')
  })
})
