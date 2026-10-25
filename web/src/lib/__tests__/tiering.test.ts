// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import {
  SECONDS_PER_DAY,
  bytesToSize,
  daysToSeconds,
  emptyPolicyForm,
  formFromPolicy,
  formToInput,
  hasCondition,
  policyConditions,
  policyFormError,
  secondsToDays,
  sharePercent,
  sizeToBytes,
} from '../tiering'

const MB = 1024 ** 2
const GB = 1024 ** 3

describe('days <-> seconds', () => {
  it('converts whole and fractional days', () => {
    expect(daysToSeconds(30)).toBe(30 * SECONDS_PER_DAY)
    expect(daysToSeconds('1.5')).toBe(129_600)
    expect(secondsToDays(30 * SECONDS_PER_DAY)).toBe(30)
    expect(secondsToDays(129_600)).toBe(1.5)
  })

  it('treats empty, zero and invalid input as no condition', () => {
    for (const v of ['', null, undefined, 0, -3, 'abc', NaN]) expect(daysToSeconds(v)).toBeNull()
    expect(secondsToDays(null)).toBeNull()
    expect(secondsToDays(0)).toBeNull()
  })
})

describe('size units', () => {
  it('converts a value and unit to bytes', () => {
    expect(sizeToBytes(10, 'MB')).toBe(10 * MB)
    expect(sizeToBytes('1.5', 'GB')).toBe(1.5 * GB)
    expect(sizeToBytes(2, 'KB')).toBe(2048)
    expect(sizeToBytes('', 'GB')).toBeNull()
    expect(sizeToBytes(0, 'TB')).toBeNull()
  })

  it('splits bytes into the largest unit keeping the value >= 1', () => {
    expect(bytesToSize(10 * MB)).toEqual({ value: 10, unit: 'MB' })
    expect(bytesToSize(1.5 * GB)).toEqual({ value: 1.5, unit: 'GB' })
    expect(bytesToSize(512)).toEqual({ value: 0.5, unit: 'KB' })
    expect(bytesToSize(null)).toEqual({ value: null, unit: 'GB' })
  })

  it('computes a bounded share', () => {
    expect(sharePercent(25, 100)).toBe(25)
    expect(sharePercent(500, 100)).toBe(100)
    expect(sharePercent(10, 0)).toBe(0)
    expect(sharePercent(undefined, 100)).toBe(0)
  })
})

describe('policy form', () => {
  it('builds the request body in seconds and bytes', () => {
    const form = emptyPolicyForm('cold')
    form.name = '  archive  '
    form.ageDays = 30
    form.idleDays = '14'
    form.minSize = { value: 10, unit: 'MB' }
    form.formats = ['maven']
    form.repoGlob = ' team-* '
    expect(formToInput(form)).toEqual({
      name: 'archive',
      enabled: true,
      target_tier: 'cold',
      from_tier: null,
      min_age_seconds: 30 * SECONDS_PER_DAY,
      min_idle_seconds: 14 * SECONDS_PER_DAY,
      min_size_bytes: 10 * MB,
      max_size_bytes: null,
      repo_glob: 'team-*',
      formats: ['maven'],
      path_glob: null,
    })
  })

  it('sends the position only when set', () => {
    const form = emptyPolicyForm('cold')
    expect('position' in formToInput(form)).toBe(false)
    form.position = 0
    expect(formToInput(form).position).toBe(0)
    form.position = ''
    expect('position' in formToInput(form)).toBe(false)
  })

  it('round-trips a stored policy without rewriting untouched values', () => {
    const stored = { id: 'p1', name: 'odd', position: 3, target_tier: 'cold', from_tier: 'hot', min_age_seconds: 3600, min_size_bytes: 1_500_001 }
    const form = formFromPolicy(stored)
    expect(formToInput(form)).toMatchObject({ position: 3, from_tier: 'hot', min_age_seconds: 3600, min_size_bytes: 1_500_001 })
    form.ageDays = 2
    expect(formToInput(form).min_age_seconds).toBe(2 * SECONDS_PER_DAY)
  })

  it('requires a name, a target and at least one condition', () => {
    const form = emptyPolicyForm('cold')
    expect(policyFormError(form)).toBe('tiering.policy.error.name')
    form.name = 'x'
    expect(policyFormError(form)).toBe('tiering.policy.error.condition')
    form.ageDays = 1
    expect(policyFormError(form)).toBeNull()
    form.targetTier = ''
    expect(policyFormError(form)).toBe('tiering.policy.error.target')
  })

  it('rejects a minimum size above the maximum', () => {
    const form = emptyPolicyForm('cold')
    form.name = 'x'
    form.minSize = { value: 2, unit: 'GB' }
    form.maxSize = { value: 1, unit: 'GB' }
    expect(policyFormError(form)).toBe('tiering.policy.error.sizeRange')
  })

  it('counts from_tier alone as a condition, like the server', () => {
    const form = emptyPolicyForm('cold')
    form.fromTier = 'hot'
    expect(hasCondition(formToInput(form))).toBe(true)
  })
})

describe('policyConditions', () => {
  it('summarises the conditions in words', () => {
    expect(
      policyConditions({
        min_age_seconds: 30 * SECONDS_PER_DAY,
        min_idle_seconds: 14 * SECONDS_PER_DAY,
        min_size_bytes: 10 * MB,
        max_size_bytes: GB,
        repo_glob: 'team-*',
        path_glob: '**/*.jar',
      }),
    ).toEqual([
      'older than 30 days',
      'not downloaded for 14 days',
      'size 10.0 MB to 1.0 GB',
      'repository team-*',
      'path **/*.jar',
    ])
  })

  it('describes one-sided size bounds', () => {
    expect(policyConditions({ min_size_bytes: GB })).toEqual(['at least 1.0 GB'])
    expect(policyConditions({ max_size_bytes: 5 * MB })).toEqual(['at most 5.0 MB'])
    expect(policyConditions({ from_tier: 'hot' })).toEqual([])
  })
})
