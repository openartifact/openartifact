// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  cleanupFormFrom,
  cleanupFormValid,
  cleanupPayload,
  cleanupPolicyFromApi,
  emptyCleanupForm,
} from '../cleanupPolicy'

describe('cleanup policy helpers', () => {
  it('derives the type from which rule is set', () => {
    const byCount = cleanupPolicyFromApi({ id: '1', name: 'a', enabled: true, keep_last_n: 5, keep_since_days: null, created_at: 't' })
    expect(byCount).toMatchObject({ type: 'keep-last-n', keep_count: 5, keep_days: undefined })
    const byAge = cleanupPolicyFromApi({ id: '2', name: 'b', enabled: false, keep_last_n: null, keep_since_days: 30, last_run_at: null, created_at: 't' })
    expect(byAge).toMatchObject({ type: 'keep-by-age', keep_days: 30, keep_count: undefined, last_run_at: undefined })
  })

  it('validates the rule of the selected type only', () => {
    const f = emptyCleanupForm()
    expect(cleanupFormValid(f)).toBe(false)
    f.name = 'x'
    expect(cleanupFormValid(f)).toBe(true)
    f.keep_count = 0
    expect(cleanupFormValid(f)).toBe(false)
    f.type = 'keep-by-age'
    expect(cleanupFormValid(f)).toBe(true)
    f.keep_days = undefined
    expect(cleanupFormValid(f)).toBe(false)
    expect(cleanupFormValid({ ...f, name: '  ', keep_days: 3 })).toBe(false)
  })

  it('sends only the rule of the selected type', () => {
    const f = { ...emptyCleanupForm(), name: 'n', keep_count: 4, keep_days: 9 }
    expect(cleanupPayload(f)).toEqual({ name: 'n', enabled: true, keep_last_n: 4 })
    expect(cleanupPayload({ ...f, type: 'keep-by-age', enabled: false })).toEqual({ name: 'n', enabled: false, keep_since_days: 9 })
  })

  it('round-trips a policy into a form', () => {
    const view = cleanupPolicyFromApi({ id: '1', name: 'a', enabled: true, keep_last_n: 5, created_at: 't' })
    expect(cleanupFormFrom(view)).toEqual({ name: 'a', type: 'keep-last-n', keep_count: 5, keep_days: undefined, enabled: true })
  })
})
