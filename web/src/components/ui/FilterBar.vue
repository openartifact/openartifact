<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="mb-4 flex flex-wrap items-center gap-2" role="search">
    <div v-if="search !== undefined" class="relative min-w-0 flex-1 basis-56 sm:max-w-sm">
      <MagnifyingGlassIcon class="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted" aria-hidden="true" />
      <input
        type="search"
        :value="search"
        :aria-label="searchLabel ?? t('ui.filter.search')"
        :placeholder="placeholder ?? t('ui.filter.search')"
        class="h-9 w-full rounded-lg border border-border-strong bg-surface py-1 pl-9 pr-3 text-sm text-foreground placeholder:text-muted focus:ring-2 focus:ring-primary max-md:h-11"
        @input="emit('update:search', ($event.target as HTMLInputElement).value)"
      />
    </div>
    <slot />
    <AppButton v-if="active" variant="ghost" size="sm" class="max-md:min-h-11" @click="emit('clear')">
      <XMarkIcon class="h-4 w-4" aria-hidden="true" />
      {{ t('ui.filter.clear') }}
    </AppButton>
    <div v-if="$slots.after" class="ml-auto flex flex-wrap items-center gap-2">
      <slot name="after" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { MagnifyingGlassIcon, XMarkIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import { t } from '../../i18n/en'

defineProps<{
  /** Text of the search input; omit to hide the input. */
  search?: string
  searchLabel?: string
  placeholder?: string
  /** True when any filter is set: shows the clear button. */
  active?: boolean
}>()

const emit = defineEmits<{
  'update:search': [value: string]
  clear: []
}>()
</script>
