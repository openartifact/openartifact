<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <TransitionRoot appear :show="ui.commandPaletteOpen" as="template">
    <Dialog as="div" class="relative z-50" @close="close">
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
                <ComboboxOptions
                  static
                  class="max-h-80 overflow-y-auto py-1"
                  role="listbox"
                >
                  <p
                    v-if="!hasAny"
                    class="px-4 py-6 text-center text-sm text-muted"
                  >
                    {{ t('palette.noResults') }}
                  </p>

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

                  <template v-if="searchResults.packages.length">
                    <p class="px-3 pt-2 pb-1 text-xs text-muted">
                      {{ t('palette.group.packages') }}
                    </p>
                    <ComboboxOption
                      v-for="pkg in searchResults.packages"
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

                  <template v-if="searchResults.repositories.length">
                    <p class="px-3 pt-2 pb-1 text-xs text-muted">
                      {{ t('palette.group.repositories') }}
                    </p>
                    <ComboboxOption
                      v-for="repo in searchResults.repositories"
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
                </ComboboxOptions>
              </Combobox>
            </DialogPanel>
          </TransitionChild>
        </div>
      </div>
    </Dialog>
  </TransitionRoot>
</template>

<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from 'vue'
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
import { search, type SearchResult } from '../api/search'
import FormatBadge from './FormatBadge.vue'
import { t } from '../i18n/en'

const ui = useUIStore()
const router = useRouter()

const query = ref('')
const searchResults = ref<SearchResult>({ packages: [], repositories: [] })
let debounceHandle: ReturnType<typeof setTimeout> | null = null
let searchGeneration = 0

const commandResults = computed<CommandEntry[]>(() => {
  const q = query.value.trim().toLowerCase()
  const all = listCommands().filter((c) => c.group === 'commands')
  if (!q) return all
  return all.filter((c) => c.title.toLowerCase().includes(q))
})

const hasAny = computed(
  () =>
    commandResults.value.length > 0 ||
    searchResults.value.packages.length > 0 ||
    searchResults.value.repositories.length > 0,
)

// Cancels the pending debounce and invalidates any in-flight request.
function resetPending(): void {
  if (debounceHandle) clearTimeout(debounceHandle)
  debounceHandle = null
  searchGeneration++
}

onBeforeUnmount(resetPending)

function onQueryChange(value: string): void {
  query.value = value
  resetPending()
  if (!value.trim()) {
    searchResults.value = { packages: [], repositories: [] }
    return
  }
  const generation = searchGeneration
  debounceHandle = setTimeout(async () => {
    debounceHandle = null
    try {
      const res = await search({ q: value, limit: 8 })
      if (generation === searchGeneration) searchResults.value = res
    } catch {
      if (generation === searchGeneration) searchResults.value = { packages: [], repositories: [] }
    }
  }, 200)
}

watch(
  () => ui.commandPaletteOpen,
  (open) => {
    if (!open) {
      resetPending()
      query.value = ''
      searchResults.value = { packages: [], repositories: [] }
    }
  },
)

function close(): void {
  ui.closeCommandPalette()
}

function packageKey(pkg: SearchResult['packages'][number]): string {
  return `pkg:${pkg.repository}:${pkg.format}:${pkg.name}`
}

// Option values are primitive keys: Headless UI compares values by reference,
// so object literals built in the template would change identity on every
// render and make each option re-register, re-rendering without end.
async function onSelect(key: string | null): Promise<void> {
  if (!key) return
  const command = commandResults.value.find((c) => `cmd:${c.id}` === key)
  const pkg = searchResults.value.packages.find((p) => packageKey(p) === key)
  const repo = searchResults.value.repositories.find((r) => `repo:${r.name}` === key)
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
  }
}
</script>
