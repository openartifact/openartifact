<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Add items to a draft: search the stored files the caller can read (by name, or `sha256:<digest>`),
  tick the ones that belong to the release. A search hit already carries the digest, path, size and
  repository an item pins, so nothing is typed by hand.
-->
<template>
  <Drawer
    :open="open"
    wide
    :title="t('bundles.items.add')"
    :eyebrow="`${name} ${version}`"
    :busy="busy"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <div class="flex flex-col gap-1">
        <label for="bundle-item-search" class="text-sm font-medium text-foreground">{{ t('bundles.items.search') }}</label>
        <input
          id="bundle-item-search"
          v-model="q"
          type="search"
          autocomplete="off"
          :placeholder="t('bundles.items.searchPlaceholder')"
          :class="INPUT_CLASS"
          @input="onInput"
          @keydown.enter.prevent="searchNow"
        />
        <p class="text-xs text-muted">{{ t('bundles.items.searchHint') }}</p>
      </div>

      <p v-if="searching" class="text-sm text-muted" role="status">{{ t('bundles.items.searching') }}</p>
      <p v-else-if="searchError" class="text-sm text-danger" role="alert">{{ searchError }}</p>
      <p v-else-if="!query" class="text-sm text-muted">{{ t('bundles.items.searchIdle') }}</p>
      <p v-else-if="rows.length === 0" class="text-sm text-muted" role="status">{{ t('bundles.items.noMatches') }}</p>

      <fieldset v-if="rows.length > 0" class="min-w-0">
        <legend class="sr-only">{{ t('bundles.items.results') }}</legend>
        <ul class="divide-y divide-border rounded-lg border border-border" data-testid="item-candidates">
          <li v-for="row in rows" :key="`${row.repository}/${row.sha256}/${row.path}`">
            <label
              class="flex min-h-11 items-start gap-3 px-3 py-2.5"
              :class="row.reason ? 'cursor-not-allowed' : 'cursor-pointer hover:bg-surface-hover'"
            >
              <input
                type="checkbox"
                class="mt-1 h-4 w-4 shrink-0 rounded border-border-strong max-md:h-5 max-md:w-5"
                :checked="picked.has(row.key)"
                :disabled="row.reason !== null"
                @change="toggle(row)"
              />
              <span class="min-w-0 flex-1">
                <span class="flex flex-wrap items-center gap-2">
                  <FormatBadge :format="row.format" size="sm" />
                  <span class="break-all font-mono text-sm font-medium text-foreground">{{ row.name }}</span>
                  <span class="font-mono text-xs text-muted">{{ row.version }}</span>
                </span>
                <span class="mt-0.5 block break-all font-mono text-xs text-muted">{{ row.path }}</span>
                <span class="mt-0.5 block text-xs text-muted">
                  {{ row.repository }} · {{ formatBytes(row.size) }}
                  <template v-if="row.reason"> · <span class="text-warning">{{ row.reason }}</span></template>
                </span>
              </span>
            </label>
          </li>
        </ul>
        <p v-if="results.length >= LIMIT" class="mt-2 text-xs text-muted">{{ t('bundles.items.refine') }}</p>
      </fieldset>

      <p v-if="error" class="text-sm text-danger" role="alert">{{ error }}</p>
    </div>

    <template #footer>
      <span class="mr-auto text-sm text-muted" role="status">
        {{ picked.size === 0 ? t('bundles.items.noneSelected') : tp('bundles.items.selected', { count: String(picked.size) }) }}
      </span>
      <AppButton variant="secondary" size="sm" :disabled="busy" @click="emit('close')">{{ t('common.cancel') }}</AppButton>
      <AppButton variant="primary" size="sm" :disabled="picked.size === 0" :loading="busy" @click="add">
        {{ picked.size === 0 ? t('bundles.items.add') : tp('bundles.items.addN', { count: String(picked.size) }) }}
      </AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import AppButton from '../AppButton.vue'
import FormatBadge from '../FormatBadge.vue'
import Drawer from '../ui/Drawer.vue'
import { addBundleItems, searchItemCandidates, type BundleItem, type ItemCandidate } from '../../api/bundles'
import type { Repository } from '../../api/repositories'
import { candidateKey, candidateToItem, isBundleable } from '../../lib/bundles'
import { INPUT_CLASS } from '../../lib/formClasses'
import { formatBytes } from '../../lib/quota'
import { useToastStore } from '../../stores/toast'
import { t, tp } from '../../i18n'

const LIMIT = 25

const props = defineProps<{
  open: boolean
  name: string
  version: string
  repos: Repository[]
  /** Items already in the bundle: their digests are not offered again. */
  existing: BundleItem[]
}>()
const emit = defineEmits<{ close: []; added: [items: BundleItem[]] }>()

const toasts = useToastStore()
const q = ref('')
const query = ref('')
const results = shallowRef<ItemCandidate[]>([])
const searching = ref(false)
const searchError = ref('')
const picked = ref(new Map<string, ItemCandidate>())
const busy = ref(false)
const error = ref('')

const repoIds = computed(() => new Map(props.repos.flatMap((r) => (r.id ? [[r.name, r.id] as const] : []))))
const have = computed(() => new Set(props.existing.map((i) => i.sha256.toLowerCase())))

interface Row extends ItemCandidate {
  key: string
  /** Why the file cannot be added; null when it can. */
  reason: string | null
}

const rows = computed<Row[]>(() => {
  const seen = new Set<string>()
  const out: Row[] = []
  for (const c of results.value) {
    const key = isBundleable(c) ? candidateKey(c) : `${c.repository}/${c.path}`
    // One blob under two names is one item: the first hit stands for it.
    if (seen.has(key) || have.value.has(key)) continue
    seen.add(key)
    const reason = !isBundleable(c)
      ? t('bundles.items.noChecksum')
      : !repoIds.value.has(c.repository)
        ? t('bundles.items.repoUnavailable')
        : null
    out.push({ ...c, key, reason })
  }
  return out
})

let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | undefined

function onInput(): void {
  clearTimeout(timer)
  timer = setTimeout(() => void search(q.value.trim()), 250)
}

function searchNow(): void {
  clearTimeout(timer)
  void search(q.value.trim())
}

async function search(text: string): Promise<void> {
  controller?.abort()
  query.value = text
  searchError.value = ''
  if (text === '') {
    results.value = []
    searching.value = false
    return
  }
  const mine = new AbortController()
  controller = mine
  searching.value = true
  try {
    const hits = await searchItemCandidates(text, mine.signal)
    if (controller !== mine) return
    results.value = hits
  } catch (err) {
    if (controller !== mine) return
    results.value = []
    searchError.value = err instanceof Error ? err.message : t('bundles.items.searchFailed')
  } finally {
    if (controller === mine) searching.value = false
  }
}

function toggle(row: Row): void {
  const next = new Map(picked.value)
  if (next.has(row.key)) next.delete(row.key)
  else next.set(row.key, row)
  picked.value = next
}

// Every opening starts from an empty search and selection.
watch(
  () => props.open,
  (open) => {
    if (!open) return
    clearTimeout(timer)
    controller?.abort()
    q.value = ''
    query.value = ''
    results.value = []
    searching.value = false
    searchError.value = ''
    picked.value = new Map()
    error.value = ''
  },
)

onBeforeUnmount(() => {
  clearTimeout(timer)
  controller?.abort()
})

async function add(): Promise<void> {
  if (busy.value || picked.value.size === 0) return
  const items = [...picked.value.values()].flatMap((c) => {
    const item = candidateToItem(c, repoIds.value)
    return item ? [item] : []
  })
  busy.value = true
  error.value = ''
  try {
    const added = await addBundleItems(props.name, props.version, items)
    toasts.success(tp('bundles.items.added', { count: String(items.length) }))
    emit('added', added)
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('bundles.items.addFailed')
  } finally {
    busy.value = false
  }
}
</script>
