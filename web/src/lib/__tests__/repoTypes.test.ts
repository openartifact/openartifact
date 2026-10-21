// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { ALL_FORMATS } from '../formats'
import { allowedFormats, coerceFormat, formatTtl, isFormatAllowed } from '../repoTypes'

describe('allowedFormats', () => {
  it('offers every format for hosted repositories', () => {
    expect(allowedFormats('hosted')).toEqual(ALL_FORMATS)
  })
  it('restricts proxy to deb, maven, raw, rpm in picker order', () => {
    expect(allowedFormats('proxy')).toEqual(['maven', 'deb', 'rpm', 'raw'])
  })
  it('restricts group to maven and raw', () => {
    expect(allowedFormats('group')).toEqual(['maven', 'raw'])
    expect(isFormatAllowed('group', 'deb')).toBe(false)
    expect(isFormatAllowed('group', 'rpm')).toBe(false)
  })
  it('does not leak the shared format list through mutation', () => {
    allowedFormats('hosted').pop()
    expect(allowedFormats('hosted')).toEqual(ALL_FORMATS)
  })
})

describe('coerceFormat', () => {
  it('keeps a supported format', () => {
    expect(coerceFormat('proxy', 'rpm')).toBe('rpm')
    expect(coerceFormat('hosted', 'docker')).toBe('docker')
  })
  it('falls back to the first allowed format when unsupported', () => {
    expect(coerceFormat('proxy', 'docker')).toBe('maven')
    expect(coerceFormat('group', 'deb')).toBe('maven')
  })
})

describe('formatTtl', () => {
  const unit = (n: number, u: string) =>
    new Intl.NumberFormat(undefined, { style: 'unit', unit: u, unitDisplay: 'long' }).format(n)

  it('renders single units', () => {
    expect(formatTtl(60)).toBe(unit(1, 'minute'))
    expect(formatTtl(3600)).toBe(unit(1, 'hour'))
    expect(formatTtl(86400)).toBe(unit(1, 'day'))
    expect(formatTtl(45)).toBe(unit(45, 'second'))
  })
  it('combines units', () => {
    expect(formatTtl(5400)).toBe(`${unit(1, 'hour')} ${unit(30, 'minute')}`)
    expect(formatTtl(90061)).toBe(
      [unit(1, 'day'), unit(1, 'hour'), unit(1, 'minute'), unit(1, 'second')].join(' '),
    )
  })
  it('renders zero and missing values as zero seconds', () => {
    expect(formatTtl(0)).toBe(unit(0, 'second'))
    expect(formatTtl(undefined)).toBe(unit(0, 'second'))
    expect(formatTtl(-5)).toBe(unit(0, 'second'))
  })
})
