// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { boolParam, enumParam, intParam, listParam, readQuery, strParam, useUrlState, writeQuery } from '../useUrlState'

const schema = {
  q: strParam(),
  page: intParam(1),
  sort: enumParam(['name', 'size'] as const, 'name'),
  flagged: boolParam(),
  type: listParam(['vuln']),
}

async function setup(path: string): Promise<{ router: Router; state: ReturnType<typeof useUrlState<typeof schema>> }> {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }] })
  await router.push(path)
  let state!: ReturnType<typeof useUrlState<typeof schema>>
  mount({ setup: () => { state = useUrlState(schema); return () => null } }, { global: { plugins: [router] } })
  return { router, state }
}

describe('codecs', () => {
  it('fall back to the default for missing, malformed or out-of-range input', () => {
    const q = { page: 'abc', sort: 'bogus', flagged: undefined }
    const v = readQuery(schema, q)
    expect(v).toEqual({ q: '', page: 1, sort: 'name', flagged: false, type: ['vuln'] })
    expect(readQuery(schema, { page: '0' }).page).toBe(1)
    expect(readQuery(schema, { page: '-3' }).page).toBe(1)
  })

  it('never write defaults to the URL', () => {
    expect(writeQuery(schema, { q: 'x', page: '3' }, { q: '', page: 1 })).toEqual({})
    expect(writeQuery(schema, {}, { q: 'x', page: 2, flagged: true, type: ['vuln', 'ai'] })).toEqual({
      q: 'x',
      page: '2',
      flagged: '1',
      type: 'vuln,ai',
    })
  })

  it('keep keys outside the schema', () => {
    expect(writeQuery(schema, { tab: 'security', q: 'old' }, { q: 'new' })).toEqual({ tab: 'security', q: 'new' })
  })
})

describe('useUrlState', () => {
  it('restores typed values from the URL', async () => {
    const { state } = await setup('/?q=left-pad&page=3&sort=size&flagged=1&type=secret,ai')
    expect(state.values.value).toEqual({ q: 'left-pad', page: 3, sort: 'size', flagged: true, type: ['secret', 'ai'] })
  })

  it('updates with replaceState by default: back goes to the entry before', async () => {
    const { router, state } = await setup('/?x=1')
    await router.push('/?x=2')
    await state.set({ q: 'b' })
    expect(router.currentRoute.value.fullPath).toBe('/?x=2&q=b')
    expect(state.values.value.q).toBe('b')
    router.back()
    await new Promise((r) => setTimeout(r, 10))
    expect(router.currentRoute.value.fullPath).toBe('/?x=1')
  })

  it('pushes a history entry on request', async () => {
    const { router, state } = await setup('/?x=1')
    await state.set({ page: 2 }, 'push')
    expect(state.values.value.page).toBe(2)
    router.back()
    await new Promise((r) => setTimeout(r, 10))
    expect(router.currentRoute.value.fullPath).toBe('/?x=1')
    expect(state.values.value.page).toBe(1)
  })

  it('resets chosen keys to their defaults and leaves the rest', async () => {
    const { router, state } = await setup('/?q=a&page=2&tab=files')
    await state.reset(['page'])
    expect(router.currentRoute.value.query).toEqual({ q: 'a', tab: 'files' })
    await state.reset()
    expect(router.currentRoute.value.query).toEqual({ tab: 'files' })
  })
})
