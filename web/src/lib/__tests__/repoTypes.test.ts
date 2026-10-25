// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { ALL_FORMATS } from '../formats'
import {
  allowedFormats,
  coerceFormat,
  federationAvailable,
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

describe('federationAvailable', () => {
  it('is true for hosted repositories of the nine capable formats', () => {
    for (const f of ['raw', 'maven', 'npm', 'docker', 'helm', 'pypi', 'cargo', 'go', 'nuget']) {
      expect(federationAvailable('hosted', f)).toBe(true)
    }
  })
  it('treats a missing type as hosted', () => {
    expect(federationAvailable(undefined, 'npm')).toBe(true)
  })
  it('is false for proxy and group repositories', () => {
    expect(federationAvailable('proxy', 'npm')).toBe(false)
    expect(federationAvailable('group', 'maven')).toBe(false)
  })
  it('is false for formats without a federation codec', () => {
    for (const f of ['deb', 'rpm', 'rubygems', 'conda', 'conan', 'ivy', 'sbt']) {
      expect(federationAvailable('hosted', f)).toBe(false)
    }
    expect(federationAvailable('hosted', undefined)).toBe(false)
  })
})
