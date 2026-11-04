// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { useLocalList, type LocalList } from '../useLocalList'
import { strParam } from '../useUrlState'

const filters = { q: strParam() }
type Row = { id: string }

describe('useLocalList', () => {
  it('keeps the newer data when a refresh overtakes a slow load', async () => {
    const loads: Array<(rows: Row[]) => void> = []
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { render: () => null } }] })
    await router.push('/')
    let local!: LocalList<typeof filters, Row>
    mount(
      {
        setup: () => {
          local = useLocalList<typeof filters, Row>({ filters, load: () => new Promise((resolve) => loads.push(resolve)) })
          return () => null
        },
      },
      { global: { plugins: [router] } },
    )
    await flushPromises()
    expect(loads).toHaveLength(1)

    const refreshed = local.refresh() // e.g. after a create, while the first load is still in flight
    await flushPromises()
    expect(loads).toHaveLength(2)

    loads[1]([{ id: 'new' }])
    await refreshed
    await flushPromises()
    loads[0]([{ id: 'stale' }]) // the first, slower response arrives last
    await flushPromises()

    expect(local.all.value).toEqual([{ id: 'new' }])
    expect(local.list.rows.value).toEqual([{ id: 'new' }])
  })
})
