// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { SERVICE_ACCOUNT_NAME, accountPath, matchesAccount } from '../serviceAccounts'

const ci = { id: 'a', name: 'ci-bot', description: 'Publishes releases', disabled: false }
const old = { id: 'b', name: 'legacy', description: '', disabled: true }

describe('SERVICE_ACCOUNT_NAME', () => {
  it('takes 3 to 64 letters, digits, dashes and underscores', () => {
    expect(SERVICE_ACCOUNT_NAME.test('ci-bot_1')).toBe(true)
    expect(SERVICE_ACCOUNT_NAME.test('ab')).toBe(false)
    expect(SERVICE_ACCOUNT_NAME.test('a'.repeat(65))).toBe(false)
    expect(SERVICE_ACCOUNT_NAME.test('has space')).toBe(false)
    expect(SERVICE_ACCOUNT_NAME.test('dot.name')).toBe(false)
  })
})

describe('matchesAccount', () => {
  it('searches name and description, ignoring case', () => {
    expect(matchesAccount(ci, 'CI-', 'all')).toBe(true)
    expect(matchesAccount(ci, 'release', 'all')).toBe(true)
    expect(matchesAccount(ci, 'zzz', 'all')).toBe(false)
    expect(matchesAccount(ci, '  ', 'all')).toBe(true)
  })

  it('filters by status', () => {
    expect(matchesAccount(old, '', 'active')).toBe(false)
    expect(matchesAccount(old, '', 'disabled')).toBe(true)
    expect(matchesAccount(ci, '', 'disabled')).toBe(false)
    expect(matchesAccount(ci, '', 'active')).toBe(true)
  })
})

describe('accountPath', () => {
  it('encodes the id', () => {
    expect(accountPath('a/b')).toBe('/settings/service-accounts/a%2Fb')
  })
})
