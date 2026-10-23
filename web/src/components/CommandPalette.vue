<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <TransitionRoot appear :show="ui.commandPaletteOpen" as="template">
    <Dialog as="div" class="relative z-50" :aria-label="t('palette.dialogLabel')" @close="close">
      <TransitionChild
        as="template"
        enter="motion-safe:duration-150 motion-safe:ease-out"
        enter-from="opacity-0"
        enter-to="opacity-100"
        leave="motion-safe:duration-100 motion-safe:ease-in"
        leave-from="opacity-100"
        leave-to="opacity-0"
      >
        <div class="fixed inset-0 bg-foreground/40" />
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
            <DialogPanel
              class="w-full max-w-xl transform rounded-lg bg-surface border border-border shadow-2xl overflow-hidden"
            >
              <Combobox
                :model-value="null"
                @update:model-value="onSelect"
              >
                <div class="flex items-center gap-2 border-b border-border px-3 py-2">
                  <MagnifyingGlassIcon class="w-4 h-4 text-muted" aria-hidden="true" />
                  <ComboboxInput
                    class="flex-1 bg-transparent text-sm text-foreground placeholder:text-muted focus:outline-none py-1"
                    :placeholder="t('palette.placeholder')"
                    :display-value="() => ''"
                    autocomplete="off"
                    @change="onQueryChange(($event.target as HTMLInputElement).value)"
                  />
                  <kbd class="text-micro text-muted border border-border rounded px-1">{{ t('palette.esc') }}</kbd>
                </div>

                <!-- Announces progress and the result count to screen readers. -->
                <p class="sr-only" role="status" aria-live="polite" aria-atomic="true">{{ announcement }}</p>

                <ComboboxOptions
                  v-if="hasOptions"
                  static
                  class="max-h-80 overflow-y-auto py-1"
                  role="listbox"
                  :aria-label="t('palette.resultsLabel')"
                >
                  <template v-if="commandResults.length">
                    <p class="px-3 pt-2 pb-1 text-xs text-muted">
                      {{ t('palette.group.commands') }}
                    </p>
                    <ComboboxOption
                      v-for="cmd in commandResults"
                      :key="`cmd:${cmd.id}`"
                      v-slot="{ active }"
                      :value="`cmd:${cmd.id}`"
                    >
                      <div
                        class="px-3 py-1.5 text-sm cursor-pointer flex items-center justify-between"
                        :class="active ? 'bg-surface-hover text-foreground' : 'text-foreground'"
                      >
                        <span>{{ cmd.title }}</span>
                        <span v-if="cmd.hint" class="text-xs text-muted">{{ cmd.hint }}</span>
                      </div>
                    </ComboboxOption>
                  </template>

                  <template v-if="packages.length">
                    <p class="px-3 pt-2 pb-1 text-xs text-muted">
                      {{ t('palette.group.packages') }}
                    </p>
                    <ComboboxOption
                      v-for="pkg in packages"
                      :key="packageKey(pkg)"
                      v-slot="{ active }"
                      :value="packageKey(pkg)"
                    >
                      <div
                        class="px-3 py-1.5 text-sm cursor-pointer flex items-center justify-between"
                        :class="active ? 'bg-surface-hover text-foreground' : 'text-foreground'"
                      >
                        <span class="flex items-center gap-2 min-w-0">
                          <FormatBadge :format="pkg.format" size="sm" />
                          <span class="truncate">{{ pkg.name }} <span class="text-muted">{{ pkg.version }}</span></span>
                        </span>
                        <span class="text-xs text-muted">{{ pkg.repository }}</span>
                      </div>
                    </ComboboxOption>
                  </template>

                  <template v-if="repositories.length">
                    <p class="px-3 pt-2 pb-1 text-xs text-muted">
                      {{ t('palette.group.repositories') }}
                    </p>
                    <ComboboxOption
                      v-for="repo in repositories"
                      :key="`repo:${repo.name}`"
                      v-slot="{ active }"
                      :value="`repo:${repo.name}`"
                    >
                      <div
                        class="px-3 py-1.5 text-sm cursor-pointer flex items-center justify-between"
                        :class="active ? 'bg-surface-hover text-foreground' : 'text-foreground'"
                      >
                        <span class="truncate">{{ repo.name }}</span>
                        <FormatBadge :format="repo.format" size="sm" />
                      </div>
                    </ComboboxOption>
                  </template>

                  <ComboboxOption
                    v-if="hasQuery"
                    v-slot="{ active }"
                    :value="VIEW_ALL"
                  >
                    <div
                      class="mt-1 px-3 py-1.5 text-sm cursor-pointer flex items-center gap-2 border-t border-border"
                      :class="active ? 'bg-surface-hover text-foreground' : 'text-muted'"
                    >
                      <MagnifyingGlassIcon class="w-4 h-4 shrink-0" aria-hidden="true" />
                      <span class="truncate">{{ viewAllLabel }}</span>
                    </div>
                  </ComboboxOption>
                </ComboboxOptions>

                <p
                  v-if="status === 'loading'"
                  class="px-4 py-3 text-sm text-muted"
                >
                  {{ t('palette.searching') }}
                </p>
                <p
                  v-else-if="status === 'tooBroad'"
                  class="px-4 py-3 text-sm text-muted"
                >
                  {{ t('palette.tooBroad') }}
                </p>
                <div
                  v-else-if="status === 'error'"
                  class="flex items-center justify-between gap-3 px-4 py-3 text-sm"
                  role="alert"
                >
                  <span class="text-danger">{{ t('palette.error') }}</span>
                  <button
                    type="button"
                    class="rounded px-2 py-1 text-xs font-medium text-foreground border border-border hover:bg-surface-hover"
                    @click="retry"
                  >
                    {{ t('palette.retry') }}
                  </button>
                </div>
                <p
                  v-else-if="!hasAny"
                  class="px-4 py-6 text-center text-sm text-muted"
                >
                  {{ t('palette.noResults') }}
                </p>
              </Combobox>
            </DialogPanel>
          </TransitionChild>
        </div>
      </div>
    </Dialog>
  </TransitionRoot>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  Dialog,
  DialogPanel,
  Combobox,
  ComboboxInput,
  ComboboxOptions,
  ComboboxOption,
  TransitionRoot,
  TransitionChild,
} from '@headlessui/vue'
import { MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import { useUIStore } from '../stores/ui'
import { listCommands, type CommandEntry } from '../composables/useCommands'
import { usePaletteSearch } from '../composables/usePaletteSearch'
import type { SearchPackageHit } from '../api/search'
import FormatBadge from './FormatBadge.vue'
import { t } from '../i18n/en'

const ui = useUIStore()
const router = useRouter()

const VIEW_ALL = 'viewall'

const query = ref('')
const { status, packages, repositories, setQuery, retry, reset } = usePaletteSearch()

const commandResults = computed<CommandEntry[]>(() => {
  const q = query.value.trim().toLowerCase()
  const all = listCommands().filter((c) => c.group === 'commands')
  if (!q) return all
  return all.filter((c) => c.title.toLowerCase().includes(q))
})

const hasQuery = computed(() => query.value.trim().length > 0)

const resultCount = computed(
  () => commandResults.value.length + packages.value.length + repositories.value.length,
)
const hasAny = computed(() => resultCount.value > 0)

// The "view all" row is an option too, so the list exists whenever there is a query.
const hasOptions = computed(() => hasAny.value || hasQuery.value)

const viewAllLabel = computed(() => t('palette.viewAll').replace('{q}', query.value.trim()))

// What the polite live region says: progress, the refusal of a too broad
// query, else how many entries are listed. Errors have their own alert.
const announcement = computed(() => {
  switch (status.value) {
    case 'loading':
      return t('palette.searching')
    case 'tooBroad':
      return t('palette.tooBroad')
    case 'error':
      return ''
    default:
      if (resultCount.value === 0) return t('palette.noResults')
      return resultCount.value === 1
        ? t('palette.count.one')
        : t('palette.count.other').replace('{n}', String(resultCount.value))
  }
})

function onQueryChange(value: string): void {
  query.value = value
  setQuery(value)
}

watch(
  () => ui.commandPaletteOpen,
  (open) => {
    if (!open) {
      reset()
      query.value = ''
    }
  },
)

function close(): void {
  ui.closeCommandPalette()
}

// One row per package version: the key includes the version so rows of a
// package never collide, even where the server lists several.
function packageKey(pkg: SearchPackageHit): string {
  return `pkg:${pkg.repository}:${pkg.format}:${pkg.name}:${pkg.version}`
}

// Option values are primitive keys: Headless UI compares values by reference,
// so object literals built in the template would change identity on every
// render and make each option re-register, re-rendering without end.
async function onSelect(key: string | null): Promise<void> {
  if (!key) return
  const command = commandResults.value.find((c) => `cmd:${c.id}` === key)
  const pkg = packages.value.find((p) => packageKey(p) === key)
  const repo = repositories.value.find((r) => `repo:${r.name}` === key)
  const searchText = query.value.trim()
  close()
  if (command) {
    await command.run()
  } else if (pkg) {
    await router.push(
      `/repos/${encodeURIComponent(pkg.repository)}/packages/${encodeURIComponent(
        pkg.format,
      )}/${encodeURIComponent(pkg.name)}`,
    )
  } else if (repo) {
    await router.push(`/repos/${encodeURIComponent(repo.name)}`)
  } else if (key === VIEW_ALL && searchText) {
    await router.push({ path: '/search', query: { q: searchText } })
  }
}
</script>
