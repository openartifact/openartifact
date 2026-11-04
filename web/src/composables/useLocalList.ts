// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { shallowRef, type ShallowRef } from 'vue'
import { useListQuery, type ListQuery, type ListQueryOptions } from './useListQuery'
import type { Schema, Values } from './useUrlState'

interface LocalListOptions<S extends Schema, Row> extends Omit<ListQueryOptions<S, Row>, 'fetch' | 'paging'> {
  /** Loads every row once (the endpoint has no filtering or paging, or the list is short by nature). */
  load: () => Promise<Row[]>
  /** True when a row passes the URL filters; omit to filter nothing. */
  match?: (row: Row, filters: Values<S>) => boolean
  /** Orders two rows by the URL's sort key; omit for the loaded order. */
  compare?: (a: Row, b: Row, sort: string) => number
}

export interface LocalList<S extends Schema, Row> {
  /** The URL-bound query to pass to `ListTable`. */
  list: ListQuery<S, Row>
  /** Every loaded row, unfiltered (for counts and lookups); empty until the first load. */
  all: ShallowRef<Row[]>
  /** Drops the cache and loads again (after a create, edit or delete). */
  refresh(): Promise<void>
}

/**
 * A `useListQuery` over a list the server returns whole: filters, sort and paging run in memory
 * while the state stays in the URL like any other list.
 */
export function useLocalList<S extends Schema, Row>(opts: LocalListOptions<S, Row>): LocalList<S, Row> {
  const all = shallowRef<Row[]>([])
  let cache: Row[] | null = null
  let pending: Promise<Row[]> | null = null
  // Bumped by `refresh`: a load started before it is superseded and must not fill the cache.
  let generation = 0

  async function rows(): Promise<Row[]> {
    while (!cache) {
      const mine = generation
      pending ??= opts.load()
      const loading = pending
      let loaded: Row[]
      try {
        loaded = await loading
      } finally {
        if (pending === loading) pending = null
      }
      if (mine !== generation) continue // superseded by a refresh: wait for the newer load
      cache = loaded
      all.value = loaded
    }
    return cache
  }

  const list = useListQuery<S, Row>({
    ...opts,
    paging: 'page',
    async fetch(req) {
      const loaded = await rows()
      let matched = opts.match ? loaded.filter((r) => opts.match!(r, req.filters)) : [...loaded]
      if (opts.compare && req.sort) {
        const sign = req.dir === 'desc' ? -1 : 1
        matched = matched.sort((a, b) => sign * opts.compare!(a, b, req.sort))
      }
      const start = (req.page - 1) * req.limit
      return { rows: matched.slice(start, start + req.limit), total: matched.length }
    },
  })

  async function refresh(): Promise<void> {
    generation++
    cache = null
    pending = null
    await list.reload()
  }

  return { list, all, refresh }
}
