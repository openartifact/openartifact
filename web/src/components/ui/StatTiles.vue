<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- A row of summary tiles: a tinted icon, a small label and a number. Size the grid with a `grid-cols-*` class. -->
<template>
  <div class="grid gap-3">
    <div
      v-for="tile in tiles"
      :key="tile.label"
      class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3"
    >
      <div class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0" :style="{ background: TONES[tile.tone].bg }">
        <component :is="tile.icon" class="w-5 h-5" :style="{ color: TONES[tile.tone].fg }" aria-hidden="true" />
      </div>
      <div class="min-w-0">
        <p class="text-micro font-mono font-semibold text-muted truncate">{{ tile.label }}</p>
        <p class="text-xl font-bold tabular-nums text-foreground leading-tight">{{ tile.value }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { Component } from 'vue'

export interface StatTile {
  label: string
  value: string | number
  icon: Component
  tone: 'primary' | 'success' | 'warning' | 'danger'
}

const TONES = {
  primary: { bg: 'rgb(var(--color-primary) / 0.1)', fg: 'rgb(var(--color-primary))' },
  success: { bg: 'rgb(var(--color-success-soft))', fg: 'rgb(var(--color-success))' },
  warning: { bg: 'rgb(var(--color-warning-soft))', fg: 'rgb(var(--color-warning))' },
  danger: { bg: 'rgb(var(--color-danger-soft))', fg: 'rgb(var(--color-danger))' },
}

defineProps<{ tiles: StatTile[] }>()
</script>
