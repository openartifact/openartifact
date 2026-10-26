// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/** A seeded, reproducible random source (mulberry32): the same seed gives the same sequence. */
export interface Prng {
  /** Float in [0, 1). */
  next(): number
  /** Integer in [0, n). */
  int(n: number): number
  /** One element of a non-empty array. */
  pick<T>(items: readonly T[]): T
  /** Index chosen with the given non-negative weights (at least one positive). */
  weighted(weights: readonly number[]): number
}

export function createPrng(seed: number): Prng {
  let a = seed >>> 0
  const next = (): number => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
  const int = (n: number): number => Math.floor(next() * n)
  return {
    next,
    int,
    pick: <T>(items: readonly T[]): T => {
      if (items.length === 0) throw new Error('pick from an empty list')
      return items[int(items.length)] as T
    },
    weighted: (weights) => {
      const total = weights.reduce((s, w) => s + w, 0)
      if (!(total > 0)) throw new Error('weighted needs a positive weight')
      let r = next() * total
      for (let i = 0; i < weights.length; i++) {
        r -= weights[i] ?? 0
        if (r < 0) return i
      }
      return weights.length - 1
    },
  }
}

/** A fresh 32-bit seed for runs that do not pin one. */
export function randomSeed(): number {
  return Math.floor(Math.random() * 0x100000000) >>> 0
}

/** Parses `MONKEY_SEED`: a non-negative integer; anything else is an error, never silently replaced. */
export function parseSeed(raw: string): number {
  if (!/^\d{1,10}$/.test(raw.trim())) throw new Error(`MONKEY_SEED must be a non-negative integer, got "${raw}"`)
  const n = Number(raw)
  if (n > 0xffffffff) throw new Error(`MONKEY_SEED must fit in 32 bits, got "${raw}"`)
  return n
}

/** Mixes a label into a seed so each role walks its own sequence yet both follow from one printed seed. */
export function deriveSeed(seed: number, label: string): number {
  let h = seed >>> 0
  for (const ch of label) h = Math.imul(h ^ ch.charCodeAt(0), 0x01000193) >>> 0
  return h
}
