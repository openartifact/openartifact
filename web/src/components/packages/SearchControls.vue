<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div>
    <FilterBar
      :search="list.text.q"
      :search-label="t('search.inputLabel')"
      :placeholder="t('search.placeholder')"
      :active="list.filtered.value"
      @update:search="list.setFilter('q', $event)"
      @clear="list.clear"
    >
      <div class="flex items-center gap-2" role="group" :aria-label="t('search.mode.label')">
        <span class="text-xs text-muted" aria-hidden="true">{{ t('search.mode.label') }}</span>
        <div class="inline-flex overflow-hidden rounded-lg border border-border-strong">
          <button
            v-for="m in MODES"
            :key="m"
            type="button"
            class="h-9 px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary max-md:h-11"
            :class="mode === m ? 'bg-primary-soft font-medium text-primary' : 'bg-surface text-muted hover:text-foreground'"
            :aria-pressed="mode === m"
            @click="setMode(m)"
          >{{ t(`search.mode.${m}` as TranslationKey) }}</button>
        </div>
      </div>
      <select :value="list.filters.value.format" :aria-label="t('search.filter.format')" :class="SELECT" @change="list.setFilter('format', pick($event))">
        <option value="">{{ t('search.filter.allFormats') }}</option>
        <option v-for="f in ALL_FORMATS" :key="f" :value="f">{{ formatLabel(f) }}</option>
      </select>
      <select :value="list.filters.value.repo" :aria-label="t('search.filter.repo')" :class="SELECT" @change="list.setFilter('repo', pick($event))">
        <option value="">{{ t('search.filter.allRepos') }}</option>
        <option v-for="r in repoOptions" :key="r" :value="r">{{ r }}</option>
      </select>
      <button
        type="button"
        class="inline-flex h-9 items-center gap-1.5 rounded-lg border px-3 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
        :class="list.filters.value.flagged ? 'border-primary bg-primary-soft text-primary' : 'border-border-strong bg-surface text-muted hover:text-foreground'"
        :aria-pressed="list.filters.value.flagged"
        @click="list.setFilter('flagged', !list.filters.value.flagged)"
      >
        <FlagIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('search.filter.flagged') }}
      </button>
    </FilterBar>
    <p v-if="autoChecksum" class="-mt-2 mb-3 flex flex-wrap items-center gap-2 text-sm text-muted" role="status" data-testid="auto-checksum">
      <InformationCircleIcon class="h-4 w-4 text-primary" aria-hidden="true" />
      {{ t('search.autoChecksum') }}
      <AppButton variant="ghost" size="xs" class="max-md:min-h-11" @click="setMode('name')">{{ t('search.autoChecksum.undo') }}</AppButton>
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { FlagIcon, InformationCircleIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import FilterBar from '../ui/FilterBar.vue'
import type { useSearch } from '../../composables/useSearch'
import { t, type TranslationKey } from '../../i18n/en'
import { ALL_FORMATS, formatLabel } from '../../lib/formats'

const props = defineProps<{
  list: ReturnType<typeof useSearch>['list']
  mode: 'name' | 'checksum'
  autoChecksum: boolean
  repos: string[]
}>()
const emit = defineEmits<{ 'update:mode': ['' | 'name' | 'checksum'] }>()

const MODES = ['name', 'checksum'] as const
const SELECT =
  'h-9 max-w-full rounded-lg border border-border-strong bg-surface px-3 text-sm text-foreground focus:ring-2 focus:ring-primary max-md:h-11'

/** The repositories the caller can read, plus one named in the URL that is not in that list. */
const repoOptions = computed(() => {
  const current = props.list.filters.value.repo
  return current && !props.repos.includes(current) ? [...props.repos, current] : props.repos
})

const pick = (e: Event): string => (e.target as HTMLSelectElement).value
const setMode = (m: 'name' | 'checksum'): void => emit('update:mode', m)
</script>
