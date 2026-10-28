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
  it('offers nix for hosted repositories only', () => {
    expect(allowedFormats('hosted')).toContain('nix')
    expect(allowedFormats('proxy')).not.toContain('nix')
    expect(allowedFormats('group')).not.toContain('nix')
  })
  it('offers pacman for hosted, proxy and group repositories', () => {
    expect(allowedFormats('hosted')).toContain('pacman')
    expect(allowedFormats('proxy')).toContain('pacman')
    expect(allowedFormats('group')).toContain('pacman')
  })
  it('offers every format for hosted repositories', () => {
    expect(allowedFormats('hosted')).toEqual(ALL_FORMATS)
  })
  it('restricts proxy to apk, cargo, conan, conda, deb, docker, go, helm, ivy, maven, npm, nuget, pacman, pypi, raw, rpm, rubygems, sbt and terraform in picker order', () => {
    expect(allowedFormats('proxy')).toEqual(['docker', 'npm', 'pypi', 'maven', 'helm', 'cargo', 'rubygems', 'deb', 'rpm', 'nuget', 'go', 'raw', 'conda', 'conan', 'ivy', 'sbt', 'terraform', 'apk', 'pacman'])
  })
  it('restricts group to apk, cargo, conan, conda, deb, docker, go, helm, ivy, maven, npm, nuget, pacman, pypi, raw, rpm, rubygems, sbt and terraform', () => {
    expect(allowedFormats('group')).toEqual(['docker', 'npm', 'pypi', 'maven', 'helm', 'cargo', 'rubygems', 'deb', 'rpm', 'nuget', 'go', 'raw', 'conda', 'conan', 'ivy', 'sbt', 'terraform', 'apk', 'pacman'])
    expect(isFormatAllowed('group', 'deb')).toBe(true)
    expect(isFormatAllowed('group', 'rpm')).toBe(true)
    expect(isFormatAllowed('group', 'conda')).toBe(true)
    expect(isFormatAllowed('group', 'bogus')).toBe(false)
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
    expect(coerceFormat('proxy', 'bogus')).toBe(allowedFormats('proxy')[0])
    expect(coerceFormat('group', 'bogus')).toBe(allowedFormats('group')[0])
  })
  it('keeps docker for proxy and group', () => {
    expect(coerceFormat('proxy', 'docker')).toBe('docker')
    expect(coerceFormat('group', 'docker')).toBe('docker')
  })
})

describe('upstream URL', () => {
  it('is optional only for docker, pypi, cargo, go, nuget, conan, rubygems and terraform (well-known defaults)', () => {
    expect(upstreamUrlRequired('terraform')).toBe(false)
    expect(upstreamUrlPlaceholder('terraform')).toBe('https://registry.terraform.io')
    expect(upstreamUrlRequired('docker')).toBe(false)
    expect(upstreamUrlRequired('pypi')).toBe(false)
    expect(upstreamUrlRequired('cargo')).toBe(false)
    expect(upstreamUrlRequired('go')).toBe(false)
    expect(upstreamUrlRequired('nuget')).toBe(false)
    expect(upstreamUrlRequired('conan')).toBe(false)
    expect(upstreamUrlRequired('rubygems')).toBe(false)
    expect(upstreamUrlRequired('maven')).toBe(true)
    expect(upstreamUrlPlaceholder('go')).toBe('https://proxy.golang.org')
    expect(upstreamUrlPlaceholder('docker')).toBe('https://registry-1.docker.io')
    expect(upstreamUrlPlaceholder('pypi')).toBe('https://pypi.org/simple')
    expect(upstreamUrlPlaceholder('cargo')).toBe('https://index.crates.io')
    expect(upstreamUrlPlaceholder('nuget')).toBe('https://api.nuget.org/v3/index.json')
    expect(upstreamUrlRequired('sbt')).toBe(true)
    expect(upstreamUrlRequired('ivy')).toBe(true)
    expect(upstreamUrlPlaceholder('conan')).toBe('https://center2.conan.io')
    expect(upstreamUrlPlaceholder('rubygems')).toBe('https://rubygems.org')
    expect(upstreamUrlRequired('pacman')).toBe(false)
    expect(upstreamUrlPlaceholder('pacman')).toBe('https://geo.mirror.pkgbuild.com')
    expect(upstreamUrlPlaceholder('apk')).toBe('https://dl-cdn.alpinelinux.org/alpine')
    expect(upstreamUrlPlaceholder('sbt')).toBe('https://repo.scala-sbt.org/scalasbt/sbt-plugin-releases')
    expect(upstreamUrlRequired('conda')).toBe(true)
    expect(upstreamUrlPlaceholder('conda')).toBe('https://conda.anaconda.org/conda-forge')
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
    for (const f of ['deb', 'rpm', 'rubygems', 'conda', 'conan', 'ivy', 'sbt', 'apk']) {
      expect(federationAvailable('hosted', f)).toBe(false)
    }
    expect(federationAvailable('hosted', undefined)).toBe(false)
  })
})
