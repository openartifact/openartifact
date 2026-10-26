// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, mock } from 'bun:test'
import { Glob } from 'bun'
import { join } from 'node:path'

// A catalog lookup while a module is evaluated freezes the string in the language
// active at import time: switching the language later cannot refresh it. Text must
// be looked up from a function, computed or template instead. This test imports
// every module of the app and fails when one calls t() while loading.

const SRC = join(import.meta.dir, '..', '..')
const real = await import('../index')
const stacks: string[] = []

const spy =
  <A extends unknown[], R>(fn: (...args: A) => R) =>
  (...args: A): R => {
    stacks.push(new Error().stack?.split('\n').slice(2, 4).join(' | ') ?? '')
    return fn(...args)
  }

mock.module('../index', () => ({
  ...real,
  t: spy(real.t),
  tp: spy(real.tp),
  tpn: spy(real.tpn),
  tDynamic: spy(real.tDynamic),
}))

const SKIP = [/__tests__/, /\/api\/generated\//, /\/pwa\/sw\.ts$/, /\/main\.ts$/, /\.d\.ts$/, /\/i18n\//]

describe('module evaluation', () => {
  it('never reads the catalog while importing', async () => {
    const files = [...new Glob('**/*.{ts,vue}').scanSync(SRC)].filter((f) => !SKIP.some((re) => re.test(`/${f}`))).sort()
    const offenders: string[] = []
    for (const file of files) {
      stacks.length = 0
      await import(join(SRC, file))
      if (stacks.length > 0) offenders.push(`${file}: ${stacks[0]}`)
    }
    expect(offenders).toEqual([])
  })
})
