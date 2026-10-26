// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { ApiError } from '../../api/client'
import { dataOf, toBlock } from '../block'

describe('toBlock', () => {
  it('wraps a result', async () => {
    const b = await toBlock(async () => 5)
    expect(b).toEqual({ state: 'ready', data: 5 })
    expect(dataOf(b)).toBe(5)
  })

  it('treats a forbidden or missing endpoint as not for this caller', async () => {
    for (const status of [401, 403, 404, 501]) {
      expect((await toBlock(async () => { throw new ApiError('no', status) })).state).toBe('forbidden')
    }
  })

  it('reports other failures as errors', async () => {
    const b = await toBlock(async () => { throw new ApiError('boom', 500) })
    expect(b).toEqual({ state: 'error', message: 'boom' })
    expect(dataOf(b)).toBeUndefined()
  })
})
