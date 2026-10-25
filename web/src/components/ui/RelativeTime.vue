<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- A timestamp as relative text ("3h", "2d", a date after `maxDays`) with the absolute time as tooltip. -->
<template>
  <time v-if="valid" :datetime="iso" :title="formatDateTime(value)" class="tabular-nums">{{ timeAgo(value, { maxDays }) }}</time>
  <span v-else>{{ fallback }}</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatDateTime, timeAgo } from '../../lib/datetime'

const props = withDefaults(
  defineProps<{ value?: string | number | Date | null; fallback?: string; maxDays?: number }>(),
  { value: undefined, fallback: '—', maxDays: 30 },
)

const date = computed(() => (props.value === null || props.value === undefined || props.value === '' ? null : new Date(props.value)))
const valid = computed(() => date.value !== null && !Number.isNaN(date.value.getTime()))
const iso = computed(() => (valid.value ? date.value!.toISOString() : undefined))
</script>
