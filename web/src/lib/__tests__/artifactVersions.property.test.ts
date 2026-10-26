// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import type { PackageItem } from '../../api/repositories'
import { compareVersionStrings, groupVersions, selectVersion } from '../artifactVersions'
import { fc, hostileString } from './propertySetup'

const token = fc.oneof(
  fc.integer({ min: 0, max: 120 }).map(String),
  fc.constantFrom('.', '.', '-', '+', '_', 'rc', 'alpha', 'beta', 'SNAPSHOT', 'dev', 'v', 'a', 'B', '01', '007', 'é'),
)
/** Strings that look like versions (1.10.0-rc.2, v2, 3.0.0+build7) and some that do not. */
const version = fc.oneof({ weight: 4, arbitrary: fc.array(token, { minLength: 1, maxLength: 8 }).map((t) => t.join('')) }, { weight: 1, arbitrary: hostileString })

describe('compareVersionStrings (property)', () => {
  it('is reflexive', () => {
    fc.assert(fc.property(version, (a) => {
      expect(compareVersionStrings(a, a)).toBe(0)
    }))
  })

  it('is antisymmetric: swapping the arguments flips the sign', () => {
    fc.assert(fc.property(version, version, (a, b) => {
      expect(Math.sign(compareVersionStrings(a, b)) === -Math.sign(compareVersionStrings(b, a))).toBe(true) // `===`: 0 and -0 are the same sign
    }))
  })

  it('is transitive', () => {
    fc.assert(fc.property(version, version, version, (a, b, c) => {
      fc.pre(compareVersionStrings(a, b) <= 0 && compareVersionStrings(b, c) <= 0)
      expect(compareVersionStrings(a, c)).toBeLessThanOrEqual(0)
      if (compareVersionStrings(a, b) < 0 || compareVersionStrings(b, c) < 0) expect(compareVersionStrings(a, c)).toBeLessThan(0)
    }))
  })

  it('orders by numeric value, not text: 1.9 before 1.10 and 2 before 10', () => {
    fc.assert(fc.property(fc.integer({ min: 0, max: 5000 }), fc.integer({ min: 1, max: 5000 }), fc.nat(9), (n, step, major) => {
      expect(compareVersionStrings(`${major}.${n}.0`, `${major}.${n + step}.0`)).toBeLessThan(0)
      expect(compareVersionStrings(`${major}.${n + step}.0`, `${major}.${n}.0`)).toBeGreaterThan(0)
    }))
  })

  it('sorts to the same permutation, idempotently, whatever the input order', () => {
    fc.assert(fc.property(fc.array(version, { maxLength: 12 }), (versions) => {
      const sorted = [...versions].sort(compareVersionStrings)
      expect([...sorted].sort()).toEqual([...versions].sort())
      expect([...sorted].sort(compareVersionStrings)).toEqual(sorted)
      for (let i = 1; i < sorted.length; i++) expect(compareVersionStrings(sorted[i - 1]!, sorted[i]!)).toBeLessThanOrEqual(0)
      // The resulting order does not depend on how the list arrived (versions that compare equal are interchangeable).
      const shuffled = [...versions].reverse().sort(compareVersionStrings)
      shuffled.forEach((v, i) => expect(compareVersionStrings(v, sorted[i]!)).toBe(0))
    }))
  })
})

const files = fc.array(
  fc.record(
    {
      id: fc.uuid(),
      name: fc.constant('pkg'),
      format: fc.constant('deb'),
      version: fc.constantFrom('1.0.0', '1.1.0', '2.0.0-rc.1', '2.0.0', '10.0.0'),
      size: fc.nat(1_000_000),
      created_at: fc.date({ min: new Date('2020-01-01'), max: new Date('2030-01-01'), noInvalidDate: true }).map((d) => d.toISOString()),
      flagged: fc.boolean(),
    },
    { requiredKeys: ['id', 'name', 'format', 'version'] },
  ),
  { maxLength: 15 },
) as fc.Arbitrary<PackageItem[]>

describe('groupVersions (property)', () => {
  it('keeps every row in exactly one version, with unique versions and summed sizes', () => {
    fc.assert(fc.property(files, (rows) => {
      const entries = groupVersions(rows)
      expect(entries.flatMap((e) => e.files.map((f) => f.id)).sort()).toEqual(rows.map((r) => r.id).sort())
      expect(new Set(entries.map((e) => e.version)).size).toBe(entries.length)
      for (const e of entries) {
        expect(e.files.every((f) => f.version === e.version)).toBe(true)
        expect(e.size).toBe(e.files.reduce((s, f) => s + (f.size ?? 0), 0))
        expect(e.flagged).toBe(e.files.some((f) => f.flagged === true))
      }
    }))
  })

  it('orders newest first and marks exactly the first entry as latest', () => {
    fc.assert(fc.property(files, (rows) => {
      const entries = groupVersions(rows)
      expect(entries.filter((e) => e.latest)).toHaveLength(entries.length ? 1 : 0)
      if (entries.length) expect(entries[0]!.latest).toBe(true)
      for (let i = 1; i < entries.length; i++) {
        expect(Date.parse(entries[i - 1]!.createdAt || '1970-01-01')).toBeGreaterThanOrEqual(Date.parse(entries[i]!.createdAt || '1970-01-01'))
      }
    }))
  })

  it('dates a version by its oldest file and is independent of the listing order of files within it', () => {
    fc.assert(fc.property(files, (all) => {
      const rows = all.filter((r) => r.created_at)
      for (const e of groupVersions(rows)) {
        const oldest = Math.min(...e.files.map((f) => Date.parse(f.created_at!)))
        expect(e.createdAt).toBe(new Date(oldest).toISOString())
      }
      const byVersion = (list: PackageItem[]) => Object.fromEntries(groupVersions(list).map((e) => [e.version, e.size]))
      expect(byVersion([...rows].reverse())).toEqual(byVersion(rows))
    }))
  })

  it('selects any listed version, the latest by default, and nothing for an unknown one', () => {
    fc.assert(fc.property(files, (rows) => {
      const entries = groupVersions(rows)
      expect(selectVersion(entries, undefined)).toBe(entries[0] ?? null)
      for (const e of entries) expect(selectVersion(entries, e.version)).toBe(e)
      expect(selectVersion(entries, 'no-such-version')).toBeNull()
    }))
  })
})
