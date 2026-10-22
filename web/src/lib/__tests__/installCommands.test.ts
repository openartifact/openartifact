// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { ALL_FORMATS } from '../formats'
import { clientSetup, pullCommand, repoBaseUrl } from '../installCommands'

describe('installCommands', () => {
  it('produces a base URL and a command for every format', () => {
    for (const f of ALL_FORMATS) {
      expect(repoBaseUrl(f, 'https://oa.example', 'r1')).toContain('oa.example')
      expect(pullCommand(f, 'https://oa.example', 'r1', 'pkg', '1.0.0').length).toBeGreaterThan(0)
    }
  })

  it('uses the native route shape for npm and maven', () => {
    expect(repoBaseUrl('npm', 'https://oa.example', 'r1')).toBe('https://oa.example/repos/r1')
    expect(repoBaseUrl('maven', 'https://oa.example', 'r1')).toBe('https://oa.example/repos/maven/r1')
  })
})

describe('clientSetup', () => {
  const origin = 'http://oa.example:8080'

  it('uses the /repos/{name} prefix for npm', () => {
    const [npm] = clientSetup('npm', origin, 'r1')
    expect(npm!.client).toBe('npm')
    expect(npm!.configure).toContain('npm config set registry http://oa.example:8080/repos/r1/')
    expect(npm!.publish).toContain('npm publish --registry http://oa.example:8080/repos/r1/')
    expect(clientSetup('npm', origin, 'r1').map((c) => c.client)).toEqual(['npm', 'pnpm', 'yarn'])
  })

  it('uses the isolated prefix for maven, sbt, ivy and raw', () => {
    for (const f of ['maven', 'sbt', 'ivy', 'raw']) {
      const text = clientSetup(f, origin, 'r1')
        .map((c) => c.configure + (c.publish ?? ''))
        .join('\n')
      expect(text).toContain(`${origin}/repos/${f}/r1`)
    }
  })

  it('uses the host root for docker', () => {
    const [d] = clientSetup('docker', origin, 'r1')
    expect(d!.configure).toBe('docker login oa.example:8080 -u <USERNAME> -p <TOKEN>')
    // The image name is the repository name: a nested path would address another repo.
    expect(d!.publish).toContain('docker push oa.example:8080/r1:latest')
    expect(d!.publish).not.toContain('/r1/')
    expect(d!.configure).not.toContain('/repos/')
  })

  it('uses /repos/{name} for other formats and never embeds real credentials', () => {
    for (const f of ['pypi', 'helm', 'cargo', 'go', 'nuget', 'deb', 'rpm', 'conda']) {
      const text = clientSetup(f, origin, 'r1').map((c) => c.configure).join('\n')
      expect(text).toContain(`${origin}/repos/r1`)
    }
  })

  it('returns snippets for every format', () => {
    for (const f of ALL_FORMATS) {
      const setups = clientSetup(f, origin, 'r1')
      expect(setups.length).toBeGreaterThan(0)
      for (const s of setups) expect(s.configure.length).toBeGreaterThan(0)
    }
  })
})
