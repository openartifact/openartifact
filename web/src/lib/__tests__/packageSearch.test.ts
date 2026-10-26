// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { checksumRow, distinctVersions, groupRow, isAutoChecksum, planSearch, type SearchFilters } from '../packageSearch'

const sha256 = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'
const base: SearchFilters = { q: '', format: '', repo: '', flagged: false, mode: '' }

describe('planSearch', () => {
  it('is idle for an empty query', () => {
    expect(planSearch(base)).toEqual({ kind: 'idle' })
    expect(planSearch({ ...base, q: '   ' })).toEqual({ kind: 'idle' })
  })

  it('searches names by default and trims the query', () => {
    expect(planSearch({ ...base, q: ' lodash ' })).toEqual({ kind: 'name', q: 'lodash' })
  })

  it('switches to a checksum lookup for a digest-shaped query', () => {
    expect(planSearch({ ...base, q: sha256.toUpperCase() })).toEqual({ kind: 'checksum', checksum: sha256 })
    expect(isAutoChecksum({ q: sha256, mode: '' })).toBe(true)
  })

  it('does not auto-detect when the mode is explicit', () => {
    expect(planSearch({ ...base, q: sha256, mode: 'name' })).toEqual({ kind: 'name', q: sha256 })
    expect(isAutoChecksum({ q: sha256, mode: 'name' })).toBe(false)
  })

  it('flags a checksum search without a full digest', () => {
    expect(planSearch({ ...base, q: 'e3b0c442', mode: 'checksum' })).toEqual({ kind: 'invalid-checksum' })
  })
})

describe('result mapping', () => {
  it('maps a grouped hit to a row linking the package page', () => {
    const row = groupRow({ repository: 'npm-main', format: 'npm', name: '@acme/ui', latest_version: '2.0.0', version_count: 4 })
    expect(row).toMatchObject({ latestVersion: '2.0.0', versionCount: 4, href: '/repos/npm-main/packages/@acme/ui', repositoryHref: '/repos/npm-main' })
    expect(row.id).toContain('@acme/ui')
  })

  it('maps a checksum hit to the version page of its artifact', () => {
    const row = checksumRow({ repository: 'go', format: 'go', name: 'example.com/x/y', version: 'v1.2.3', filename: 'y.zip', checksum: sha256, algorithm: 'sha256' })
    expect(row.href).toBe('/repos/go/packages/example.com/x/y/versions/v1.2.3')
    expect(row).toMatchObject({ filename: 'y.zip', algorithm: 'sha256' })
  })

  it('lists a version once, newest first', () => {
    const rows = distinctVersions([
      { id: 'a', version: '1.0.0', created_at: '2026-01-01T00:00:00Z' },
      { id: 'b', version: '1.0.0', created_at: '2026-01-02T00:00:00Z' },
      { id: 'c', version: '2.0.0', created_at: '2026-02-01T00:00:00Z' },
    ])
    expect(rows.map((r) => r.version)).toEqual(['2.0.0', '1.0.0'])
    expect(rows[1]!.created_at).toBe('2026-01-02T00:00:00Z')
  })
})
