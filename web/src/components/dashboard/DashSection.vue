<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  One block of the Home page: a heading with an optional link and the three states every
  block has (loading, error with retry, empty). The default slot is the loaded content.
-->
<template>
  <section class="mt-8 first:mt-0" :aria-labelledby="headingId" :aria-busy="state === 'loading' || undefined">
    <div class="mb-2 flex flex-wrap items-center gap-2">
      <h2 :id="headingId" class="text-base font-semibold text-foreground">{{ title }}</h2>
      <slot name="badge" />
      <span class="flex-1" />
      <slot name="actions" />
    </div>
    <div v-if="state === 'loading'" class="space-y-2 rounded-lg border border-border bg-surface p-4" role="status">
      <span class="sr-only">{{ t('common.loading') }}</span>
      <div v-for="n in 3" :key="n" class="h-4 animate-pulse rounded bg-surface-hover" aria-hidden="true" />
    </div>
    <div v-else-if="state === 'error'" class="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-surface p-4 text-sm" role="alert">
      <span class="text-danger">{{ error || t('dashboard.loadFailed') }}</span>
      <AppButton variant="secondary" size="sm" class="max-md:min-h-11" @click="emit('retry')">{{ t('dashboard.retry') }}</AppButton>
    </div>
    <div v-else-if="empty" class="rounded-lg border border-border bg-surface">
      <slot name="empty" />
    </div>
    <slot v-else />
  </section>
</template>

<script setup lang="ts">
import AppButton from '../AppButton.vue'
import { t } from '../../i18n'

withDefaults(
  defineProps<{
    title: string
    headingId: string
    state: 'loading' | 'ready' | 'error'
    empty?: boolean
    error?: string
  }>(),
  { empty: false, error: '' },
)
const emit = defineEmits<{ retry: [] }>()
</script>
