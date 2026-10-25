// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { formatDuration, formatExactDuration } from '../duration'

const unit = (n: number, u: string, display: 'long' | 'short' = 'long') =>
  new Intl.NumberFormat('en', { style: 'unit', unit: u, unitDisplay: display }).format(n)

describe('formatDuration', () => {
  it('renders single units', () => {
    expect(formatDuration(60)).toBe(unit(1, 'minute'))
    expect(formatDuration(3600)).toBe(unit(1, 'hour'))
    expect(formatDuration(86400)).toBe(unit(1, 'day'))
    expect(formatDuration(45)).toBe(unit(45, 'second'))
  })
  it('combines units', () => {
    expect(formatDuration(5400)).toBe(`${unit(1, 'hour')} ${unit(30, 'minute')}`)
    expect(formatDuration(90061)).toBe(
      [unit(1, 'day'), unit(1, 'hour'), unit(1, 'minute'), unit(1, 'second')].join(' '),
    )
  })
  it('keeps only the leading units when maxUnits is set', () => {
    expect(formatDuration(90061, { display: 'short', maxUnits: 2 })).toBe(
      `${unit(1, 'day', 'short')} ${unit(1, 'hour', 'short')}`,
    )
  })
  it('renders zero, negative and missing values as zero seconds', () => {
    expect(formatDuration(0)).toBe(unit(0, 'second'))
    expect(formatDuration(undefined)).toBe(unit(0, 'second'))
    expect(formatDuration(-5)).toBe(unit(0, 'second'))
  })
})

describe('formatExactDuration', () => {
  it('uses the largest unit that divides exactly', () => {
    expect(formatExactDuration(86_400)).toBe('1 day')
    expect(formatExactDuration(30 * 86_400)).toBe('30 days')
    expect(formatExactDuration(7200)).toBe('2 hours')
    expect(formatExactDuration(120)).toBe('2 minutes')
    expect(formatExactDuration(90)).toBe('90 seconds')
  })
})
