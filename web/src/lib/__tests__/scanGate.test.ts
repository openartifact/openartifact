// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { SecurityFinding } from '../../api/security'
import { epssInvalid, gateCriteria, reasonParts, unionFindings } from '../scanGate'

const f = (id: string, priority: string) => ({ id, priority }) as unknown as SecurityFinding

describe('gateCriteria', () => {
  it('is null while no condition is named', () => {
    expect(gateCriteria({ kev: false, epssPercent: '' })).toBeNull()
    expect(gateCriteria({ kev: false, epssPercent: '0' })).toBeNull()
    expect(gateCriteria({ kev: false, epssPercent: 'abc' })).toBeNull()
    expect(gateCriteria({ kev: false, epssPercent: '101' })).toBeNull()
  })
  it('turns a percentage into a 0..1 EPSS minimum', () => {
    expect(gateCriteria({ kev: false, epssPercent: ' 10 ' })).toEqual({ kev: undefined, epss_min: 0.1 })
  })
  it('combines KEV and EPSS', () => {
    expect(gateCriteria({ kev: true, epssPercent: '50' })).toEqual({ kev: true, epss_min: 0.5 })
    expect(gateCriteria({ kev: true, epssPercent: 'x' })).toEqual({ kev: true, epss_min: undefined })
  })
})

describe('epssInvalid', () => {
  it('accepts an empty field and percentages in (0, 100]', () => {
    for (const s of ['', '  ', '0.5', '100']) expect(epssInvalid({ kev: false, epssPercent: s })).toBe(false)
  })
  it('rejects text, zero, negatives and over 100', () => {
    for (const s of ['abc', '0', '-1', '100.1']) expect(epssInvalid({ kev: false, epssPercent: s })).toBe(true)
  })
})

describe('unionFindings', () => {
  it('lists a scan once and puts the most urgent first', () => {
    const out = unionFindings([[f('a', 'epss'), f('b', 'kev')], [f('a', 'epss'), f('c', 'high')]])
    expect(out.map((x) => x.id)).toEqual(['b', 'a', 'c'])
  })
  it('is empty for no lists', () => {
    expect(unionFindings([])).toEqual([])
  })
})

describe('reasonParts', () => {
  it('splits the reason formats', () => {
    expect(reasonParts('kev')).toEqual({ kind: 'kev', value: '' })
    expect(reasonParts('epss>=0.1')).toEqual({ kind: 'epss', value: '0.1' })
    expect(reasonParts('priority>=high')).toEqual({ kind: 'priority', value: 'high' })
    expect(reasonParts('weird')).toEqual({ kind: 'other', value: 'weird' })
  })
})
