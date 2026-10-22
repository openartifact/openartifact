<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="flex items-center justify-between px-4 py-3 border-t border-border bg-surface">
    <span class="text-sm text-muted">
      {{ t('pagination.showing') }} {{ from }}–{{ to }} {{ t('pagination.of') }} {{ total }}
    </span>
    <div class="flex gap-2">
      <AppButton
        variant="secondary"
        size="sm"
        :disabled="page <= 1"
        :aria-label="t('pagination.previous')"
        @click="emit('change', page - 1)"
      >
        {{ t('pagination.previous') }}
      </AppButton>
      <AppButton
        variant="secondary"
        size="sm"
        :disabled="page * limit >= total"
        :aria-label="t('pagination.next')"
        @click="emit('change', page + 1)"
      >
        {{ t('pagination.next') }}
      </AppButton>
    </div>
  </div>
</template>

<script setup lang="ts">
import AppButton from './AppButton.vue'
import { computed } from 'vue'
import { t } from '../i18n/en'

const props = defineProps<{
  total: number
  page: number
  limit: number
}>()

const emit = defineEmits<{
  change: [page: number]
}>()

const from = computed(() => Math.min((props.page - 1) * props.limit + 1, props.total))
const to = computed(() => Math.min(props.page * props.limit, props.total))
</script>
