<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The command palette: the primary way to move around. A combobox (the input)
  controlling a listbox of grouped results; the focus never leaves the input,
  the active result is announced through aria-activedescendant. Pages, settings
  and actions are static and appear at once; repositories, packages, projects,
  bundles and users are fetched and added as they arrive. Loaded lazily by App.
-->
<template>
  <TransitionRoot appear :show="ui.commandPaletteOpen" as="template" @after-leave="onAfterLeave">
    <Dialog as="div" class="relative z-50" :aria-label="t('shell.palette.dialogLabel')" @close="close">
      <TransitionChild
        as="template"
        enter="motion-safe:duration-150 motion-safe:ease-out"
        enter-from="opacity-0"
        enter-to="opacity-100"
        leave="motion-safe:duration-100 motion-safe:ease-in"
        leave-from="opacity-100"
        leave-to="opacity-0"
      >
        <div class="fixed inset-0 bg-foreground/40" aria-hidden="true" />
      </TransitionChild>

      <div class="fixed inset-0 overflow-y-auto">
        <div class="flex min-h-full items-start justify-center p-4 pt-[10vh]">
          <TransitionChild
            as="template"
            enter="motion-safe:duration-150 motion-safe:ease-out"
            enter-from="opacity-0 scale-95"
            enter-to="opacity-100 scale-100"
            leave="motion-safe:duration-100 motion-safe:ease-in"
            leave-from="opacity-100 scale-100"
            leave-to="opacity-0 scale-95"
          >
            <DialogPanel class="w-full max-w-xl transform overflow-hidden rounded-lg border border-border bg-surface shadow-2xl" data-testid="palette">
              <div class="flex items-center gap-2 border-b border-border px-3 py-2">
                <MagnifyingGlassIcon class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />
                <input
                  v-model="query"
                  type="text"
                  role="combobox"
                  class="min-h-9 flex-1 bg-transparent py-1 text-sm text-foreground placeholder:text-muted focus:outline-none"
                  :placeholder="t('shell.palette.placeholder')"
                  :aria-label="t('shell.palette.inputLabel')"
                  :aria-expanded="flat.length > 0"
                  aria-controls="palette-list"
                  aria-autocomplete="list"
                  :aria-activedescendant="activeItem ? optionId(activeItem) : undefined"
                  autocomplete="off"
                  spellcheck="false"
                  data-testid="palette-input"
                  @keydown="onKeydown"
                />
                <kbd class="rounded border border-border px-1 text-micro text-muted" aria-hidden="true">{{ t('shell.palette.esc') }}</kbd>
              </div>

              <!-- Announces the result count (and why a locked result did nothing) to screen readers. -->
              <p class="sr-only" role="status" aria-live="polite" aria-atomic="true" data-testid="palette-status">{{ announcement }}</p>

              <div
                id="palette-list"
                role="listbox"
                :aria-label="t('shell.palette.resultsLabel')"
                class="nice-scroll max-h-96 overflow-y-auto py-1"
              >
                <div v-for="g in rows" :key="g.id" role="group" :aria-labelledby="`palette-group-${g.id}`">
                  <p :id="`palette-group-${g.id}`" class="px-3 pb-1 pt-2 text-xs text-muted">{{ t(GROUP_TITLE[g.id]) }}</p>
                  <div
                    v-for="row in g.items"
                    :id="optionId(row.item)"
                    :key="row.item.id"
                    role="option"
                    :aria-selected="row.index === active"
                    :aria-disabled="row.item.locked ? 'true' : undefined"
                    class="flex min-h-11 cursor-pointer items-center gap-2.5 px-3 py-1.5 text-sm md:min-h-9"
                    :class="[
                      row.index === active ? 'bg-surface-hover' : '',
                      row.item.locked ? 'text-muted' : 'text-foreground',
                    ]"
                    :data-testid="`palette-option-${row.item.id}`"
                    @mousedown.prevent
                    @mousemove="active = row.index"
                    @click="activate(row.item)"
                  >
                    <FormatBadge v-if="row.item.format" :format="row.item.format" size="sm" class="shrink-0" />
                    <component :is="paletteIcon(row.item.icon)" v-else class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />
                    <span class="min-w-0 flex-1">
                      <span class="block truncate" :class="row.item.mono && 'font-mono'">
                        <template v-for="(part, i) in highlightParts(row.item.title, parsed.text)" :key="i">
                          <mark v-if="part.match" class="bg-transparent font-semibold text-foreground underline decoration-primary underline-offset-2">{{ part.text }}</mark>
                          <template v-else>{{ part.text }}</template>
                        </template>
                      </span>
                      <span v-if="row.item.description" class="block truncate text-xs text-muted" :class="row.item.id === 'search:checksum' && 'font-mono'">{{ row.item.description }}</span>
                    </span>
                    <span v-if="itemHint(row.item)" class="shrink-0 text-xs text-muted">{{ itemHint(row.item) }}</span>
                  </div>
                </div>
              </div>

              <p v-if="flat.length === 0 && search.status.value !== 'loading'" class="px-4 py-6 text-center text-sm text-muted">
                <template v-if="parsed.mode === 'actions'">{{ t('shell.palette.noActions') }}</template>
                <template v-else>
                  <strong class="block font-medium text-foreground">{{ t('shell.palette.noResults') }}</strong>
                  {{ t('shell.palette.noResultsHint') }}
                </template>
              </p>
              <p v-if="search.status.value === 'loading'" class="px-4 py-2 text-xs text-muted">{{ t('shell.palette.searching') }}</p>
              <p v-else-if="search.status.value === 'tooBroad'" class="px-4 py-2 text-xs text-muted">{{ t('shell.palette.tooBroad') }}</p>
              <div v-else-if="search.status.value === 'error'" class="flex items-center justify-between gap-3 px-4 py-2 text-xs" role="alert">
                <span class="text-danger">{{ t('shell.palette.partialError') }}</span>
                <button
                  type="button"
                  class="min-h-11 rounded border border-border px-2 py-1 font-medium text-foreground hover:bg-surface-hover md:min-h-7"
                  @click="search.retry()"
                >{{ t('shell.palette.retry') }}</button>
              </div>

              <div class="hidden items-center gap-4 border-t border-border px-3 py-2 text-xs text-muted sm:flex" aria-hidden="true">
                <span><kbd class="mr-0.5 rounded border border-border px-1">↑</kbd><kbd class="mr-1 rounded border border-border px-1">↓</kbd>{{ t('shell.palette.footNavigate') }}</span>
                <span><kbd class="mr-1 rounded border border-border px-1">↵</kbd>{{ t('shell.palette.footOpen') }}</span>
                <span><kbd class="mr-1 rounded border border-border px-1">&gt;</kbd>{{ t('shell.palette.footActions') }}</span>
              </div>
            </DialogPanel>
          </TransitionChild>
        </div>
      </div>
    </Dialog>
  </TransitionRoot>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Dialog, DialogPanel, TransitionRoot, TransitionChild } from '@headlessui/vue'
import { MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import FormatBadge from './FormatBadge.vue'
import { paletteIcon } from './navIcons'
import { useUIStore } from '../stores/ui'
import { useSessionStore } from '../stores/session'
import { useThemeStore } from '../stores/theme'
import { useSignOut } from '../composables/useSignOut'
import { usePaletteSearch } from '../composables/usePaletteSearch'
import {
  buildGroups,
  flattenGroups,
  highlightParts,
  itemHint,
  moveActive,
  parseQuery,
  type PaletteGroupId,
  type PaletteItem,
} from '../lib/palette'
import { loadRecent, recentStorageKey, type RecentItem } from '../lib/paletteRecent'
import { t, tp, type TranslationKey } from '../i18n/en'

const ui = useUIStore()
const session = useSessionStore()
const theme = useThemeStore()
const router = useRouter()
const signOut = useSignOut()

const GROUP_TITLE: Record<PaletteGroupId, TranslationKey> = {
  recent: 'shell.palette.group.recent',
  pages: 'shell.palette.group.pages',
  repositories: 'shell.palette.group.repositories',
  packages: 'shell.palette.group.packages',
  projects: 'shell.palette.group.projects',
  bundles: 'shell.palette.group.bundles',
  settings: 'shell.palette.group.settings',
  users: 'shell.palette.group.users',
  actions: 'shell.palette.group.actions',
  search: 'shell.palette.group.search',
}

const query = ref('')
const active = ref(0)
const recent = ref<RecentItem[]>([])
const lockedMessage = ref('')

const search = usePaletteSearch({ isAdmin: () => session.isAdmin })
const parsed = computed(() => parseQuery(query.value))

const groups = computed(() =>
  buildGroups({
    query: parsed.value,
    context: { isAdmin: session.isAdmin, canCreateRepo: session.canCreateRepo },
    recent: recent.value,
    entities: search.entities.value,
  }),
)
const flat = computed(() => flattenGroups(groups.value))
const activeItem = computed<PaletteItem | undefined>(() => flat.value[active.value])

// Each row knows its index in the flat list, which is what `active` points at.
const rows = computed(() => {
  let index = 0
  return groups.value.map((g) => ({ id: g.id, items: g.items.map((item) => ({ item, index: index++ })) }))
})

const optionId = (item: PaletteItem): string => `palette-opt-${item.id.replace(/[^a-zA-Z0-9_-]/g, '_')}`

const announcement = computed(() => {
  if (lockedMessage.value) return lockedMessage.value
  if (search.status.value === 'loading') return t('shell.palette.searching')
  const n = flat.value.length
  if (n === 0) return t('shell.palette.noResults')
  return n === 1 ? t('shell.palette.count.one') : tp('shell.palette.count.other', { n: String(n) })
})

watch(query, (text) => {
  lockedMessage.value = ''
  active.value = 0
  const p = parseQuery(text)
  search.setQuery(p.text, p.checksum !== null || p.mode === 'actions')
})

// Results that arrive later must not move the user's selection: keep the same item active.
watch(flat, (now, before) => {
  const id = before[active.value]?.id
  if (!id || now[active.value]?.id === id) return
  const i = now.findIndex((item) => item.id === id)
  active.value = i >= 0 ? i : Math.min(active.value, Math.max(now.length - 1, 0))
})

watch(active, async () => {
  await nextTick()
  const item = activeItem.value
  if (item) document.getElementById(optionId(item))?.scrollIntoView?.({ block: 'nearest' })
})

watch(
  () => ui.commandPaletteOpen,
  (open) => {
    if (!open) return
    recent.value = loadRecent(window.localStorage, recentStorageKey(session.user?.id))
    active.value = 0
  },
  { immediate: true },
)

function onAfterLeave(): void {
  query.value = ''
  search.reset()
}

function close(): void {
  ui.closeCommandPalette()
}

function onKeydown(e: KeyboardEvent): void {
  if (e.isComposing) return
  if (e.key === 'Enter') {
    e.preventDefault()
    void activate(activeItem.value)
    return
  }
  const next = moveActive(e.key, active.value, flat.value.length)
  if (next !== null) {
    e.preventDefault()
    active.value = next
    lockedMessage.value = ''
  }
}

async function activate(item: PaletteItem | undefined): Promise<void> {
  if (!item) return
  if (item.locked) {
    lockedMessage.value = tp('shell.palette.lockedAnnounce', { title: item.title })
    return
  }
  close()
  if (item.action === 'toggle-theme') theme.setMode(theme.resolved === 'dark' ? 'light' : 'dark')
  else if (item.action === 'sign-out') await signOut()
  else if (item.to) await router.push(item.to)
}
</script>
