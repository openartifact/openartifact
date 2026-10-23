// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { ALL_FORMATS } from '../formats'
import {
  allowedFormats,
  coerceFormat,
  formatTtl,
  isFormatAllowed,
  upstreamUrlPlaceholder,
  upstreamUrlRequired,
} from '../repoTypes'

describe('allowedFormats', () => {
  it('offers every format for hosted repositories', () => {
    expect(allowedFormats('hosted')).toEqual(ALL_FORMATS)
  })
  it('restricts proxy to cargo, deb, docker, go, helm, maven, npm, nuget, pypi, raw, rpm in picker order', () => {
    expect(allowedFormats('proxy')).toEqual(['docker', 'npm', 'pypi', 'maven', 'helm', 'cargo', 'deb', 'rpm', 'nuget', 'go', 'raw'])
  })
  it('restricts group to cargo, deb, docker, go, helm, maven, npm, nuget, pypi, raw and rpm', () => {
    expect(allowedFormats('group')).toEqual(['docker', 'npm', 'pypi', 'maven', 'helm', 'cargo', 'deb', 'rpm', 'nuget', 'go', 'raw'])
    expect(isFormatAllowed('group', 'deb')).toBe(true)
    expect(isFormatAllowed('group', 'rpm')).toBe(true)
    expect(isFormatAllowed('group', 'conda')).toBe(false)
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
    expect(coerceFormat('proxy', 'conda')).toBe(allowedFormats('proxy')[0])
    expect(coerceFormat('group', 'conda')).toBe(allowedFormats('group')[0])
  })
  it('keeps docker for proxy and group', () => {
    expect(coerceFormat('proxy', 'docker')).toBe('docker')
    expect(coerceFormat('group', 'docker')).toBe('docker')
  })
})

describe('upstream URL', () => {
  it('is optional only for docker, pypi, cargo, go and nuget (well-known defaults)', () => {
    expect(upstreamUrlRequired('docker')).toBe(false)
    expect(upstreamUrlRequired('pypi')).toBe(false)
    expect(upstreamUrlRequired('cargo')).toBe(false)
    expect(upstreamUrlRequired('go')).toBe(false)
    expect(upstreamUrlRequired('nuget')).toBe(false)
    expect(upstreamUrlRequired('maven')).toBe(true)
    expect(upstreamUrlPlaceholder('go')).toBe('https://proxy.golang.org')
    expect(upstreamUrlPlaceholder('docker')).toBe('https://registry-1.docker.io')
    expect(upstreamUrlPlaceholder('pypi')).toBe('https://pypi.org/simple')
    expect(upstreamUrlPlaceholder('cargo')).toBe('https://index.crates.io')
    expect(upstreamUrlPlaceholder('nuget')).toBe('https://api.nuget.org/v3/index.json')
  })
})

describe('formatTtl', () => {
  const unit = (n: number, u: string) =>
    new Intl.NumberFormat('en', { style: 'unit', unit: u, unitDisplay: 'long' }).format(n)

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
