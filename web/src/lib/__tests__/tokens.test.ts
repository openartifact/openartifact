// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  defaultExpiry,
  expiresAtFor,
  expiresBeforeFor,
  expiryChoices,
  expiryOf,
  expiryText,
  grantableTokenRoles,
  nameProblem,
  offeredTypes,
  presetType,
  scopeOf,
  toListParams,
} from '../tokens'

const NOW = Date.parse('2030-06-01T12:00:00Z')
const DAY = 86_400_000
const at = (days: number): string => new Date(NOW + days * DAY).toISOString()

describe('list filters', () => {
  it('maps "all" and empty values to no filter', () => {
    expect(toListParams({ type: 'all', q: '  ', expiring: '', project: '' }, NOW)).toEqual({
      type: undefined,
      q: undefined,
      expiresBefore: undefined,
      project: undefined,
    })
  })

  it('maps the quick filters to an expires_before instant', () => {
    expect(expiresBeforeFor('7', NOW)).toBe(at(7))
    expect(expiresBeforeFor('30', NOW)).toBe(at(30))
    expect(toListParams({ type: 'project', q: ' ci ', expiring: '30', project: 'web' }, NOW)).toEqual({
      type: 'project',
      q: 'ci',
      expiresBefore: at(30),
      project: 'web',
    })
  })
})

describe('expiry state', () => {
  it('never expires without expires_at', () => {
    expect(expiryOf({ expired: false }, NOW)).toEqual({ kind: 'never' })
    expect(expiryText({ kind: 'never' })).toBe('No expiry')
  })

  it('flags expired, soon and ok in words', () => {
    expect(expiryOf({ expires_at: at(-3), expired: true }, NOW)).toEqual({ kind: 'expired', days: 3 })
    expect(expiryText({ kind: 'expired', days: 3 })).toBe('Expired 3 days ago')
    expect(expiryText({ kind: 'expired', days: 1 })).toBe('Expired yesterday')
    expect(expiryText({ kind: 'expired', days: 0 })).toBe('Expired today')
    expect(expiryOf({ expires_at: at(5), expired: false }, NOW)).toEqual({ kind: 'soon', days: 5 })
    expect(expiryText({ kind: 'soon', days: 5 })).toBe('Expires in 5 days')
    expect(expiryOf({ expires_at: at(60), expired: false }, NOW)).toEqual({ kind: 'ok', days: 60 })
  })

  it('trusts the server flag over the clock', () => {
    expect(expiryOf({ expires_at: at(10), expired: true }, NOW).kind).toBe('expired')
  })
})

describe('create drawer rules', () => {
  it('offers personal always, project with a project, federation to admins', () => {
    expect(offeredTypes({ isAdmin: false, hasProjects: false })).toEqual(['personal'])
    expect(offeredTypes({ isAdmin: false, hasProjects: true })).toEqual(['personal', 'project'])
    expect(offeredTypes({ isAdmin: true, hasProjects: true })).toEqual(['personal', 'project', 'federation'])
  })

  it('presets only an offered type', () => {
    expect(presetType('project', ['personal', 'project'])).toBe('project')
    expect(presetType('federation', ['personal', 'project'])).toBe('personal')
    expect(presetType(undefined, ['personal'])).toBe('personal')
  })

  it('lets viewers mint viewer tokens only', () => {
    expect(grantableTokenRoles('viewer')).toEqual(['viewer'])
    expect(grantableTokenRoles('developer')).toEqual(['viewer', 'developer'])
    expect(grantableTokenRoles('owner')).toEqual(['viewer', 'developer'])
    expect(grantableTokenRoles(undefined)).toEqual(['viewer'])
  })

  it('switches the lifetimes with the type and the project cap', () => {
    expect(expiryChoices('personal', 0)).toEqual([30, 90, 365, 0])
    expect(expiryChoices('federation', 0)).toEqual([])
    expect(expiryChoices('project', 0)).toEqual([30, 90, 365, 0])
    expect(expiryChoices('project', 90)).toEqual([30, 90])
    expect(expiryChoices('project', 45)).toEqual([30, 45])
    expect(expiryChoices('project', 7)).toEqual([7])
    // a project cap never limits personal tokens
    expect(expiryChoices('personal', 7)).toEqual([30, 90, 365, 0])
  })

  it('defaults to 90 days, else the longest allowed', () => {
    expect(defaultExpiry([30, 90, 365, 0])).toBe(90)
    expect(defaultExpiry([30, 45])).toBe(45)
    expect(defaultExpiry([])).toBe(0)
    expect(expiresAtFor(0, NOW)).toBeUndefined()
    expect(expiresAtFor(30, NOW)).toBe(at(30))
  })

  it('validates names per type', () => {
    expect(nameProblem('personal', ' ')).toBe('required')
    expect(nameProblem('personal', 'a'.repeat(101))).toBe('tooLong')
    expect(nameProblem('personal', 'my laptop')).toBeNull()
    expect(nameProblem('federation', 'my laptop')).toBe('invalid')
    expect(nameProblem('federation', 'peer-1.eu')).toBeNull()
    expect(nameProblem('federation', 'a'.repeat(64))).toBe('tooLong')
  })
})

describe('scope cell', () => {
  it('describes each type from the fields the server knows', () => {
    const base = { id: '1', name: 'n', owner: 'ada', created_at: at(0), expired: false }
    expect(scopeOf({ ...base, type: 'personal', scopes: ['a', 'b'] })).toEqual({ kind: 'personal', scopes: 2 })
    expect(scopeOf({ ...base, type: 'personal' })).toEqual({ kind: 'personal', scopes: 0 })
    expect(scopeOf({ ...base, type: 'project', project: 'web', role: 'developer' })).toEqual({ kind: 'project', project: 'web', role: 'developer' })
    expect(scopeOf({ ...base, type: 'federation', repository: 'libs', project: 'web' })).toEqual({ kind: 'federation', repository: 'libs', project: 'web' })
  })
})
