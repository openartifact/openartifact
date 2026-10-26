// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import router from '../../router'
import { resolveCrumbs } from '../crumbs'
import { fc, hostileWord } from './propertySetup'

// router.resolve() matches records without running guards or loading pages, so
// every declared trail can be exercised against URLs built from its own path.
const records = router
  .getRoutes()
  .filter((r) => !r.redirect && r.name !== 'not-found' && r.path !== '/:pathMatch(.*)*')
const withCrumbs = records.filter((r) => r.meta.crumbs?.length)
const withoutCrumbs = records.filter((r) => !r.meta.crumbs?.length)

/** A path segment value: never contains '/', never a dot segment (those are normalised away by URL resolution). */
const segment = hostileWord.filter((s) => !s.includes('/') && s !== '.' && s !== '..')

/** Parameter names of a route path with whether they span slashes (`:pkg(.+)`). */
function paramsOf(path: string): { name: string; multi: boolean }[] {
  return [...path.matchAll(/:(\w+)(\([^)]*\))?/g)].map((m) => ({ name: m[1]!, multi: m[2] === '(.+)' }))
}

/** Values for every param of a record, then the concrete URL and the raw values. */
function instance(path: string, values: string[][]): { url: string; raw: Record<string, string> } {
  const raw: Record<string, string> = {}
  let k = 0
  const url = path.replace(/:(\w+)(\([^)]*\))?/g, (_, name: string, re?: string) => {
    const segs = values[k++]!.slice(0, re === '(.+)' ? undefined : 1)
    raw[name] = segs.join('/')
    return segs.map((s) => encodeURIComponent(s)).join('/')
  })
  return { url, raw }
}

const instances = (record: (typeof records)[number]) =>
  fc
    .array(fc.array(segment, { minLength: 1, maxLength: 3 }), { minLength: paramsOf(record.path).length, maxLength: paramsOf(record.path).length })
    .map((values) => instance(record.path, values))

describe('route crumb trails (property)', () => {
  it('declares a trail on every page except the dashboard and the blank-layout screens', () => {
    expect(withoutCrumbs.map((r) => r.name).sort()).toEqual(['change-password', 'dashboard', 'login'])
    expect(withCrumbs.length).toBeGreaterThan(5)
  })

  it('gives top-level pages without a declared trail no crumbs at all', () => {
    for (const r of withoutCrumbs) {
      fc.assert(fc.property(instances(r), ({ url }) => {
        expect(resolveCrumbs(router.resolve(url))).toEqual([])
      }))
    }
  })

  for (const record of withCrumbs) {
    describe(`${String(record.name)} (${record.path})`, () => {
      it('resolves one non-empty crumb per declared spec, with param labels equal to the raw route values', () => {
        fc.assert(fc.property(instances(record), ({ url, raw }) => {
          const route = router.resolve(url)
          const specs = route.meta.crumbs!
          const crumbs = resolveCrumbs(route)
          expect(crumbs).toHaveLength(specs.length)
          crumbs.forEach((c, i) => {
            expect(c.label.length).toBeGreaterThan(0)
            if (specs[i]!.param) expect(c.label).toBe(raw[specs[i]!.param!]!)
          })
        }))
      })

      it('never lists the current page among its own ancestors, nor the same crumb twice in a row', () => {
        fc.assert(fc.property(instances(record), ({ url }) => {
          const route = router.resolve(url)
          const crumbs = resolveCrumbs(route)
          const ancestors = crumbs.slice(0, -1)
          for (const a of ancestors) {
            expect(a.to).toBeDefined()
            expect(decodeURIComponent(a.to!)).not.toBe(decodeURIComponent(route.path))
          }
          crumbs.slice(1).forEach((c, i) => {
            expect(c.label === crumbs[i]!.label && c.to === crumbs[i]!.to).toBe(false)
          })
        }))
      })

      it('links every ancestor to a real page that is a path prefix of the current one', () => {
        fc.assert(fc.property(instances(record), ({ url }) => {
          const route = router.resolve(url)
          const here = decodeURIComponent(route.path)
          for (const a of resolveCrumbs(route).slice(0, -1)) {
            const target = router.resolve(a.to!)
            expect(target.name).not.toBe('not-found')
            expect(target.matched.length).toBeGreaterThan(0)
            const there = decodeURIComponent(a.to!)
            expect(here === there || here.startsWith(`${there}/`)).toBe(true)
          }
        }))
      })
    })
  }

  it('never throws for any in-app path, even one no route declares', () => {
    fc.assert(fc.property(fc.array(segment, { maxLength: 5 }), (segs) => {
      const route = router.resolve(`/${segs.map((s) => encodeURIComponent(s)).join('/')}`)
      const crumbs = resolveCrumbs(route)
      for (const c of crumbs) expect(typeof c.label).toBe('string')
    }))
  })
})
