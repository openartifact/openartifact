<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The one list. Typed columns with a slot per cell (`#cell-<key>="{ row, value }"`),
  optional sorting and selection, a loading skeleton, and empty / filtered-empty /
  error states. Paging is one prop: `paging="page"` (page + limit) or
  `paging="cursor"` ("load more"). Below 768px every row becomes a compact card:
  the first cell is the title (with the actions cell beside it) and the other cells
  flow on shared lines, each led by its column label.

  Either drive it from `useListQuery` (`:query`, everything wired, URL-bound) or
  pass `rows`/`loading`/`error`/... and listen to the events.
-->
<template>
  <div :aria-busy="loading || undefined">
    <!-- first load: skeleton -->
    <div v-if="loading && rows.length === 0 && !error" role="status" class="rounded-lg border border-border bg-surface" data-testid="list-loading">
      <span class="sr-only">{{ t('ui.table.loading') }}</span>
      <div v-for="n in 5" :key="n" class="flex items-center gap-4 border-b border-border px-4 py-4 last:border-b-0" aria-hidden="true">
        <div v-for="c in Math.min(columns.length, 4)" :key="c" class="h-3 flex-1 animate-pulse rounded bg-surface-hover" />
      </div>
    </div>

    <div v-else-if="error && rows.length === 0" class="rounded-lg border border-border bg-surface">
      <StateBlock variant="error" :hint="error">
        <template #action>
          <AppButton variant="secondary" @click="retry">{{ t('ui.state.retry') }}</AppButton>
        </template>
      </StateBlock>
    </div>

    <div v-else-if="rows.length === 0" class="rounded-lg border border-border bg-surface">
      <StateBlock v-if="filtered" variant="filtered">
        <template #action>
          <AppButton variant="secondary" @click="clearFilters">{{ t('ui.state.clearFilters') }}</AppButton>
        </template>
      </StateBlock>
      <StateBlock v-else variant="empty" :title="emptyTitle" :hint="emptyHint" :command="emptyCommand">
        <template v-if="$slots['empty-action']" #action><slot name="empty-action" /></template>
      </StateBlock>
    </div>

    <div v-else class="overflow-hidden rounded-lg border border-border bg-surface" :class="{ 'opacity-70': loading }">
      <div v-if="selectable && selected.length > 0" class="border-b border-border bg-primary-soft px-4 py-2 text-sm text-primary" role="status">
        {{ tp('ui.table.selected', { count: String(selected.length) }) }}
      </div>
      <TableScroller :fit-from="768">
        <table class="min-w-full divide-y divide-border max-md:block">
          <caption class="sr-only">{{ caption }}</caption>
          <thead class="bg-surface-hover max-md:sr-only">
            <tr>
              <th v-if="selectable" scope="col" class="w-10 px-4 py-3">
                <input
                  ref="allBox"
                  type="checkbox"
                  :checked="allSelected"
                  :aria-label="t('ui.table.selectAll')"
                  class="h-4 w-4 rounded border-border-strong"
                  @change="toggleAll"
                />
              </th>
              <th
                v-for="col in columns"
                :key="col.key"
                scope="col"
                class="px-4 py-3 text-xs font-medium text-muted"
                :class="[col.align === 'end' ? 'text-right' : 'text-left', col.actions ? ACTIONS_HEAD_MD : '']"
                :data-drop-order="col.dropOrder"
                :aria-sort="col.sortable ? ariaSort(col.key) : undefined"
              >
                <span v-if="col.actions" class="sr-only">{{ t('ui.table.actions') }}</span>
                <button
                  v-else-if="col.sortable"
                  type="button"
                  class="-mx-1 inline-flex items-center gap-1 rounded px-1 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                  @click="toggleSort(col.key)"
                >
                  {{ col.label }}
                  <component :is="sortIcon(col.key)" class="h-3.5 w-3.5" aria-hidden="true" />
                </button>
                <template v-else>{{ col.label }}</template>
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border max-md:block">
            <tr
              v-for="(row, index) in rows"
              :key="keyOf(row)"
              class="group/row hover:bg-surface-hover max-md:flex max-md:flex-wrap max-md:items-center max-md:gap-x-4 max-md:px-4 max-md:py-3 max-md:after:order-2 max-md:after:basis-full max-md:after:content-['']"
            >
              <td v-if="selectable" class="w-10 px-4 py-3 max-md:order-first max-md:w-auto max-md:px-0 max-md:py-0">
                <input
                  type="checkbox"
                  :checked="selected.includes(keyOf(row))"
                  :aria-label="t('ui.table.selectRow')"
                  class="h-4 w-4 rounded border-border-strong max-md:h-5 max-md:w-5"
                  @change="toggleRow(keyOf(row))"
                />
              </td>
              <td
                v-for="(col, ci) in columns"
                :key="col.key"
                :data-label="col.actions || col.hideLabel || ci === 0 ? '' : col.label"
                class="px-4 py-3 text-sm text-foreground max-md:flex max-md:min-w-0 max-md:items-baseline max-md:gap-1.5 max-md:px-0 max-md:py-0.5 max-md:before:shrink-0 max-md:before:text-xs max-md:before:text-muted max-md:before:content-[attr(data-label)]"
                :class="[
                  col.align === 'end' ? 'text-right max-md:text-left' : '',
                  cellPlacement(col, ci),
                  col.actions ? ACTIONS_CELL_MD : '',
                  col.grow ? GROW_CELL_MD : '',
                ]"
              >
                <slot :name="`cell-${col.key}`" :row="row" :value="row[col.key]" :index="index">
                  {{ row[col.key] }}
                </slot>
              </td>
            </tr>
          </tbody>
        </table>
      </TableScroller>

      <div class="flex flex-wrap items-center justify-between gap-3 border-t border-border px-4 py-3 text-sm text-muted">
        <span data-testid="list-summary">{{ summary }}</span>
        <div v-if="paging === 'page' && pages > 1" class="flex items-center gap-2">
          <AppButton variant="secondary" size="sm" class="max-md:h-11" :disabled="currentPage <= 1" @click="goto(currentPage - 1)">
            {{ t('ui.table.previous') }}
          </AppButton>
          <span class="text-xs">{{ tp('ui.table.page', { page: String(currentPage), pages: String(pages) }) }}</span>
          <AppButton variant="secondary" size="sm" class="max-md:h-11" :disabled="currentPage >= pages" @click="goto(currentPage + 1)">
            {{ t('ui.table.next') }}
          </AppButton>
        </div>
        <AppButton v-else-if="paging === 'cursor' && more" variant="secondary" size="sm" class="max-md:h-11" :loading="loading" @click="loadMore">
          {{ t('ui.table.loadMore') }}
        </AppButton>
      </div>
      <p v-if="error" role="alert" class="border-t border-border px-4 py-2 text-sm text-danger">{{ error }}</p>
    </div>
  </div>
</template>

<script setup lang="ts" generic="Row extends Record<string, any>">
import { computed, ref, watchEffect } from 'vue'
import { ChevronDownIcon, ChevronUpDownIcon, ChevronUpIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import StateBlock from './StateBlock.vue'
import TableScroller from './TableScroller.vue'
import { ACTIONS_CELL_MD, ACTIONS_HEAD_MD, GROW_CELL_MD } from './tableStyles'
import type { ListQuery } from '../../composables/useListQuery'
import type { Schema } from '../../composables/useUrlState'
import { t, tp } from '../../i18n'

export interface ListColumn {
  key: string
  label: string
  sortable?: boolean
  align?: 'start' | 'end'
  /** Row-actions column: the header is announced as "Actions" and the cell has no stacked label. */
  actions?: boolean
  /** Self-explanatory cell (a title, a package link): below 768px it flows without its column label. */
  hideLabel?: boolean
  /** Low-value column: when the table does not fit, columns with a `dropOrder` are hidden, 1 first, until it does. Never set on the identifier or the actions. The mobile card shows every cell. */
  dropOrder?: number
  /** The identifier column: takes the leftover width and truncates (put `truncate` on the content) instead of stretching the table. */
  grow?: boolean
}

const props = withDefaults(
  defineProps<{
    columns: ListColumn[]
    /** Accessible table name. */
    caption: string
    /** A `useListQuery` result: supplies rows, states, paging and sort, and receives page/sort/clear. */
    query?: ListQuery<Schema, Row>
    rows?: Row[]
    rowKey?: string | ((row: Row) => string)
    loading?: boolean
    error?: string
    /** Total matches (page paging). */
    total?: number
    page?: number
    limit?: number
    paging?: 'page' | 'cursor'
    hasMore?: boolean
    sort?: string
    dir?: 'asc' | 'desc'
    /** Filters are active: an empty result is "no matches" (with a clear button), not "nothing yet". */
    filtered?: boolean
    selectable?: boolean
    selected?: string[]
    emptyTitle?: string
    emptyHint?: string
    emptyCommand?: string
  }>(),
  {
    query: undefined,
    rows: () => [],
    rowKey: 'id',
    error: '',
    total: undefined,
    page: 1,
    limit: 25,
    paging: 'page',
    sort: '',
    dir: 'asc',
    selected: () => [],
    emptyTitle: undefined,
    emptyHint: undefined,
    emptyCommand: undefined,
  },
)

const emit = defineEmits<{
  'update:page': [page: number]
  'update:selected': [keys: string[]]
  sort: [key: string]
  'load-more': []
  retry: []
  clear: []
}>()

// `query` wins over the loose props, so a page binds either one.
const rows = computed<Row[]>(() => props.query?.rows.value ?? props.rows)
const loading = computed(() => props.query?.loading.value ?? props.loading ?? false)
const error = computed(() => props.query?.error.value ?? props.error)
const filtered = computed(() => props.query?.filtered.value ?? props.filtered ?? false)
const paging = computed(() => props.query?.paging ?? props.paging)
const currentPage = computed(() => props.query?.page.value ?? props.page)
const limit = computed(() => props.query?.limit ?? props.limit)
const total = computed(() => props.query?.total.value ?? props.total ?? rows.value.length)
const more = computed(() => props.query?.hasMore.value ?? props.hasMore ?? false)
const sort = computed(() => props.query?.sort.value ?? props.sort)
const dir = computed(() => props.query?.dir.value ?? props.dir)
const pages = computed(() => Math.max(1, Math.ceil(total.value / limit.value)))

const summary = computed(() => {
  if (paging.value === 'cursor') return tp('ui.table.showingCount', { count: String(rows.value.length) })
  const from = (currentPage.value - 1) * limit.value + 1
  const to = Math.min(from + rows.value.length - 1, total.value)
  return tp('ui.table.showing', { from: String(from), to: String(to), total: String(total.value) })
})

// Mobile card layout: the first cell is the title (order 0, takes the free width), the actions cell sits
// beside it (order 1), a zero-height break (the row's ::after, order 2) ends that line, every other cell
// follows (order 3) and wraps.
function cellPlacement(col: ListColumn, index: number): string {
  if (col.actions) return 'max-md:order-1 max-md:items-center max-md:py-0 max-md:before:hidden'
  if (index === 0) return 'max-md:order-none max-md:min-h-11 max-md:flex-1 max-md:items-center max-md:font-medium max-md:before:hidden'
  return col.hideLabel ? 'max-md:order-3 max-md:before:hidden' : 'max-md:order-3'
}

function keyOf(row: Row): string {
  return typeof props.rowKey === 'function' ? props.rowKey(row) : String(row[props.rowKey])
}

function goto(page: number): void {
  if (props.query) props.query.setPage(page)
  else emit('update:page', page)
}
function toggleSort(key: string): void {
  if (props.query) props.query.setSort(key)
  else emit('sort', key)
}
function loadMore(): void {
  if (props.query) void props.query.loadMore()
  else emit('load-more')
}
function retry(): void {
  if (props.query) void props.query.reload()
  else emit('retry')
}
function clearFilters(): void {
  if (props.query) props.query.clear()
  emit('clear')
}

function ariaSort(key: string): 'ascending' | 'descending' | 'none' {
  if (sort.value !== key) return 'none'
  return dir.value === 'desc' ? 'descending' : 'ascending'
}
function sortIcon(key: string) {
  if (sort.value !== key) return ChevronUpDownIcon
  return dir.value === 'desc' ? ChevronDownIcon : ChevronUpIcon
}

// Selection
const allBox = ref<HTMLInputElement | null>(null)
const allSelected = computed(() => rows.value.length > 0 && rows.value.every((r) => props.selected.includes(keyOf(r))))
watchEffect(() => {
  if (allBox.value) allBox.value.indeterminate = props.selected.length > 0 && !allSelected.value
})
function toggleRow(key: string): void {
  emit('update:selected', props.selected.includes(key) ? props.selected.filter((k) => k !== key) : [...props.selected, key])
}
function toggleAll(): void {
  emit('update:selected', allSelected.value ? [] : rows.value.map(keyOf))
}
</script>
