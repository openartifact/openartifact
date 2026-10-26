// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { formatBuildTime, formatCommit, isStamped, isUnstampedBuild, LDFLAGS_EXAMPLE } from '../buildInfo'
import { formatDateTime } from '../datetime'

describe('build info formatting', () => {
  it('treats empty and "unknown" as unstamped', () => {
    for (const v of [undefined, null, '', '  ', 'unknown', 'UNKNOWN']) expect(isStamped(v)).toBe(false)
    expect(isStamped('d6fdb07')).toBe(true)
  })

  it('shortens full SHAs to 7 characters and passes other stamps through', () => {
    expect(formatCommit('d6fdb0717477dac564cd07d01adf9ccfd99c52b2')).toBe('d6fdb07')
    expect(formatCommit('d6fdb07')).toBe('d6fdb07')
    expect(formatCommit('v0.4.1-rc1')).toBe('v0.4.1-rc1')
    expect(formatCommit('unknown')).toBeNull()
    expect(formatCommit(undefined)).toBeNull()
  })

  it('formats RFC 3339 build times and degrades gracefully', () => {
    expect(formatBuildTime('2026-10-21T22:34:06Z')).toBe(formatDateTime('2026-10-21T22:34:06Z'))
    expect(formatBuildTime('2026-10-21T22:34:06.431Z')).toBe(formatDateTime('2026-10-21T22:34:06.431Z'))
    expect(formatBuildTime('Tuesday')).toBe('Tuesday')
    expect(formatBuildTime('unknown')).toBeNull()
    expect(formatBuildTime('')).toBeNull()
  })
})

describe('isUnstampedBuild', () => {
  it('flags a dev or unknown version and an unknown commit', () => {
    expect(isUnstampedBuild({ version: 'dev', commit: 'd6fdb07' })).toBe(true)
    expect(isUnstampedBuild({ version: 'unknown', commit: 'unknown' })).toBe(true)
    expect(isUnstampedBuild({ version: '', commit: 'd6fdb07' })).toBe(true)
    expect(isUnstampedBuild({ version: 'v1.2.3', commit: 'unknown' })).toBe(true)
  })

  it('accepts a stamped release', () => {
    expect(isUnstampedBuild({ version: 'v1.2.3', commit: 'd6fdb07abcdef' })).toBe(false)
  })

  it('offers the flags the build files use', () => {
    expect(LDFLAGS_EXAMPLE).toContain('-X main.version=')
    expect(LDFLAGS_EXAMPLE).toContain('-X main.commit=')
    expect(LDFLAGS_EXAMPLE).toContain('-X main.buildTime=')
  })
})
