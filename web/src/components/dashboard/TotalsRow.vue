<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <dl v-if="items.length > 0" class="mt-8 flex flex-wrap gap-x-8 gap-y-2 border-t border-border pt-4 text-sm" :aria-label="t('dashboard.totals.label')" data-testid="totals">
    <div v-for="item in items" :key="item.key" class="flex items-baseline gap-2">
      <dd class="order-first font-semibold tabular-nums text-foreground">{{ item.value }}</dd>
      <dt class="text-muted"><router-link :to="item.to" class="hover:text-foreground hover:underline">{{ item.label }}</router-link></dt>
    </div>
  </dl>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { t, type TranslationKey } from '../../i18n/en'

const props = defineProps<{
  totals: { repositories?: number; formats?: number; projects?: number; users?: number }
}>()

const LINKS = {
  repositories: '/repos',
  formats: '/repos',
  projects: '/projects',
  users: '/settings/users',
} as const

/** Only totals the API supplied are shown. */
const items = computed(() =>
  (Object.keys(LINKS) as (keyof typeof LINKS)[])
    .filter((k) => typeof props.totals[k] === 'number')
    .map((k) => ({ key: k, value: props.totals[k], to: LINKS[k], label: t(`dashboard.totals.${k}` as TranslationKey) })),
)
</script>
