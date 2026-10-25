// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onScopeDispose, reactive, ref, shallowRef, watch, type ComputedRef, type Ref } from 'vue'
import { useRoute } from 'vue-router'
import {
  enumParam,
  intParam,
  readQuery,
  useUrlState,
  type Schema,
  type Values,
} from './useUrlState'

export interface ListRequest<S extends Schema> {
  filters: Values<S>
  /** 1-based page (page paging). */
  page: number
  limit: number
  /** Opaque cursor of the next chunk (cursor paging); undefined for the first one. */
  cursor?: string
  sort: string
  dir: 'asc' | 'desc'
}

export interface ListResponse<Row> {
  rows: Row[]
  /** Total matches (page paging). */
  total?: number
  /** More rows exist after this chunk (cursor paging). */
  hasMore?: boolean
  nextCursor?: string
}

export interface ListQueryOptions<S extends Schema, Row> {
  /** URL-bound filter parameters. Free-text ones should be listed in `debounce`. */
  filters: S
  fetch: (req: ListRequest<S>) => Promise<ListResponse<Row>>
  /** `page`: page + limit (page number in the URL). `cursor`: "load more" chunks, nothing but filters in the URL. */
  paging?: 'page' | 'cursor'
  limit?: number
  /** Filter keys typed as text: the input updates at once, the URL (and the fetch) follow after `debounceMs`. */
  debounce?: (keyof S & string)[]
  debounceMs?: number
  /** Columns the table can sort by; the first is the default. Omit for an unsorted list. */
  sorts?: readonly string[]
  defaultDir?: 'asc' | 'desc'
}

export interface ListQuery<S extends Schema, Row> {
  rows: Ref<Row[]>
  total: Ref<number>
  loading: Ref<boolean>
  error: Ref<string>
  /** Filters as committed to the URL. */
  filters: ComputedRef<Values<S>>
  /** Live value of debounced text filters (bind inputs to this). */
  text: Record<string, string>
  page: ComputedRef<number>
  limit: number
  paging: 'page' | 'cursor'
  hasMore: Ref<boolean>
  sort: ComputedRef<string>
  dir: ComputedRef<'asc' | 'desc'>
  /** True when at least one filter differs from its default (drives filtered-empty vs empty). */
  filtered: ComputedRef<boolean>
  setFilter<K extends keyof S & string>(key: K, value: Values<S>[K]): void
  setFilters(patch: Partial<Values<S>>): void
  setPage(page: number): void
  setSort(key: string): void
  loadMore(): Promise<void>
  clear(): void
  reload(): Promise<void>
}

const PAGE = 'page'
const SORT = 'sort'
const DIR = 'dir'

/**
 * A list bound to the URL: filters, page and sort are restored from the query
 * string, filter changes reset the page, text filters are debounced, and a
 * response that arrives after a newer request was issued is dropped.
 */
export function useListQuery<S extends Schema, Row>(opts: ListQueryOptions<S, Row>): ListQuery<S, Row> {
  const paging = opts.paging ?? 'page'
  const limit = opts.limit ?? 25
  const debounceMs = opts.debounceMs ?? 250
  const debounced = new Set<string>(opts.debounce ?? [])
  const sorts = opts.sorts ?? []
  const defaultDir = opts.defaultDir ?? 'asc'

  const schema = {
    ...opts.filters,
    ...(paging === 'page' ? { [PAGE]: intParam(1) } : {}),
    ...(sorts.length ? { [SORT]: enumParam(sorts, sorts[0]), [DIR]: enumParam(['asc', 'desc'] as const, defaultDir) } : {}),
  } as Schema
  const url = useUrlState(schema)
  const route = useRoute()

  const filterKeys = Object.keys(opts.filters)
  const filters = computed(() => {
    const all = url.values.value as Record<string, unknown>
    return Object.fromEntries(filterKeys.map((k) => [k, all[k]])) as Values<S>
  })
  const page = computed(() => (paging === 'page' ? ((url.values.value as Record<string, number>)[PAGE] ?? 1) : 1))
  const sort = computed(() => (url.values.value as Record<string, string>)[SORT] ?? '')
  const dir = computed(() => ((url.values.value as Record<string, 'asc' | 'desc'>)[DIR] ?? defaultDir))
  const filtered = computed(() =>
    filterKeys.some((k) => opts.filters[k].serialize((filters.value as Record<string, unknown>)[k]) !== null),
  )

  const rows = shallowRef<Row[]>([]) as Ref<Row[]>
  const total = ref(0)
  const loading = ref(false)
  const error = ref('')
  const hasMore = ref(false)
  let nextCursor: string | undefined
  let generation = 0

  let timer: ReturnType<typeof setTimeout> | undefined
  let pending: Record<string, unknown> | null = null
  onScopeDispose(() => clearTimeout(timer))

  // Text drafts: what the input shows. Re-synced from the URL on back/forward.
  const text = reactive<Record<string, string>>({})
  for (const k of debounced) text[k] = String((filters.value as Record<string, unknown>)[k] ?? '')
  watch(filters, (next) => {
    for (const k of debounced) {
      const committed = String((next as Record<string, unknown>)[k] ?? '')
      if (pending === null && text[k] !== committed) text[k] = committed
    }
  })

  function commit(patch: Record<string, unknown>): void {
    // A filter change always returns to the first page and drops any cursor.
    const next = paging === 'page' ? { ...patch, [PAGE]: 1 } : patch
    void url.set(next as Partial<Values<S>>)
  }

  function flush(extra: Record<string, unknown> = {}): void {
    clearTimeout(timer)
    const merged = { ...(pending ?? {}), ...extra }
    pending = null
    commit(merged)
  }

  function setFilters(patch: Partial<Values<S>>): void {
    const immediate: Record<string, unknown> = {}
    let typed = false
    for (const [k, v] of Object.entries(patch)) {
      if (debounced.has(k)) {
        text[k] = String(v ?? '')
        pending = { ...(pending ?? {}), [k]: v }
        typed = true
      } else {
        immediate[k] = v
      }
    }
    // A non-text change applies at once and carries any pending text with it.
    if (Object.keys(immediate).length > 0 || !typed) {
      flush(immediate)
      return
    }
    clearTimeout(timer)
    timer = setTimeout(() => flush(), debounceMs)
  }

  function setFilter<K extends keyof S & string>(key: K, value: Values<S>[K]): void {
    setFilters({ [key]: value } as unknown as Partial<Values<S>>)
  }

  function setPage(n: number): void {
    if (paging === 'page') void url.set({ [PAGE]: Math.max(1, n) } as unknown as Partial<Values<S>>)
  }

  function setSort(key: string): void {
    if (!sorts.includes(key)) return
    const nextDir = sort.value === key ? (dir.value === 'asc' ? 'desc' : 'asc') : defaultDir
    const patch = { [SORT]: key, [DIR]: nextDir, ...(paging === 'page' ? { [PAGE]: 1 } : {}) }
    void url.set(patch as unknown as Partial<Values<S>>)
  }

  function clear(): void {
    clearTimeout(timer)
    pending = null
    for (const k of debounced) text[k] = String(opts.filters[k].default ?? '')
    const patch: Record<string, unknown> = {}
    for (const k of filterKeys) patch[k] = opts.filters[k].default
    if (paging === 'page') patch[PAGE] = 1
    void url.set(patch as Partial<Values<S>>)
  }

  async function run(append: boolean): Promise<void> {
    const mine = ++generation
    loading.value = true
    error.value = ''
    try {
      const res = await opts.fetch({
        filters: filters.value,
        page: page.value,
        limit,
        cursor: append ? nextCursor : undefined,
        sort: sort.value,
        dir: dir.value,
      })
      if (mine !== generation) return
      rows.value = append ? [...rows.value, ...res.rows] : res.rows
      total.value = res.total ?? rows.value.length
      hasMore.value = res.hasMore ?? false
      nextCursor = res.nextCursor
    } catch (err) {
      if (mine !== generation) return
      if (!append) rows.value = []
      error.value = err instanceof Error ? err.message : String(err)
    } finally {
      if (mine === generation) loading.value = false
    }
  }

  async function loadMore(): Promise<void> {
    if (paging !== 'cursor' || !hasMore.value || loading.value) return
    await run(true)
  }

  // Refetch only when a parameter of this list changes, not on unrelated query keys (?tab=).
  const key = computed(() => {
    const q = readQuery(schema, route.query)
    return JSON.stringify(Object.keys(schema).map((k) => schema[k].serialize(q[k])))
  })
  watch(key, () => void run(false), { immediate: true })

  return {
    rows,
    total,
    loading,
    error,
    filters,
    text,
    page,
    limit,
    paging,
    hasMore,
    sort,
    dir,
    filtered,
    setFilter,
    setFilters,
    setPage,
    setSort,
    loadMore,
    clear,
    reload: () => run(false),
  }
}
