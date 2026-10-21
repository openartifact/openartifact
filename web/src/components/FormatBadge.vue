<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <span class="inline-flex items-center rounded-md bg-surface-hover text-foreground/80 text-xs font-mono font-medium overflow-hidden" :class="sizeClass">
    <span v-if="icon" class="inline-flex items-center shrink-0 ml-1.5 text-[color:var(--fmt)]" :class="{ 'dark:text-foreground/85': lowOnDark }" :style="{ '--fmt': color }">
      <svg :viewBox="viewBox" :class="iconSize" aria-hidden="true"><path :d="icon" fill="currentColor" :fill-rule="evenodd ? 'evenodd' : undefined" /></svg>
    </span>
    <span v-else class="w-[3px] self-stretch shrink-0" :style="{ backgroundColor: color }"></span>
    <span class="px-2 py-0.5">{{ label }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatLabel, formatColor, formatColorLowOnDark, formatIcon, formatIconEvenOdd, formatViewBox } from '../lib/formats'

const props = defineProps<{
  format: string
  size?: 'sm' | 'md'
}>()

const label = computed(() => formatLabel(props.format))
const color = computed(() => formatColor(props.format))
const lowOnDark = computed(() => formatColorLowOnDark(props.format))
const icon = computed(() => formatIcon(props.format))
const evenodd = computed(() => formatIconEvenOdd(props.format))
const viewBox = computed(() => formatViewBox(props.format))
const iconSize = computed(() => (props.size === 'sm' ? 'w-3 h-3' : 'w-3.5 h-3.5'))
const sizeClass = computed(() => (props.size === 'sm' ? 'text-[11px]' : ''))
</script>
