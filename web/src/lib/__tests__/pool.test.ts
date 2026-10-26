// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { mapPool } from '../pool'

describe('mapPool', () => {
  it('keeps the input order and reports failures without stopping the rest', async () => {
    const res = await mapPool([1, 2, 3, 4], 2, async (n) => {
      if (n === 2) throw new Error('boom')
      return n * 10
    })
    expect(res.map((r) => (r.ok ? r.value : 'x'))).toEqual([10, 'x', 30, 40])
  })

  it('never exceeds the concurrency', async () => {
    let running = 0
    let peak = 0
    await mapPool(Array.from({ length: 10 }, (_, i) => i), 3, async () => {
      running++
      peak = Math.max(peak, running)
      await new Promise((r) => setTimeout(r, 2))
      running--
    })
    expect(peak).toBeLessThanOrEqual(3)
    expect(peak).toBeGreaterThan(1)
  })

  it('handles an empty list', async () => {
    expect(await mapPool([], 4, async () => 1)).toEqual([])
  })
})
