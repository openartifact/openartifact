<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <router-link
    :to="to"
    class="group relative flex items-center gap-3 w-full h-8 rounded-md text-[13px] font-medium tracking-tight transition-[background,color] duration-150"
    :class="[
      collapsed ? 'justify-center px-0' : 'px-2.5',
      isActive
        ? 'bg-surface-hover text-foreground'
        : 'text-foreground/75 hover:text-foreground hover:bg-surface-hover',
    ]"
  >
    <component
      :is="icon"
      class="w-[18px] h-[18px] shrink-0"
      :class="isActive ? 'text-foreground' : 'text-muted group-hover:text-foreground'"
      aria-hidden="true"
    />

    <span v-if="!collapsed" class="truncate flex-1">{{ label }}</span>

    <span
      v-if="badge != null && !collapsed"
      class="text-[10px] font-mono font-semibold text-muted bg-surface-hover px-1.5 py-0.5 rounded"
    >{{ badge }}</span>

    <!-- Tooltip shown only when sidebar is collapsed -->
    <span
      v-if="collapsed"
      class="pointer-events-none absolute left-full ml-2 px-2 py-1 rounded-md bg-foreground text-background text-xs whitespace-nowrap opacity-0 -translate-x-1 group-hover:opacity-100 group-hover:translate-x-0 transition-all duration-150 z-50 shadow-lg"
    >{{ label }}</span>
  </router-link>
</template>

<script setup lang="ts">
import type { Component } from 'vue'
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const props = defineProps<{
  to: string
  label: string
  icon: Component
  collapsed?: boolean
  badge?: string | number
}>()

const route = useRoute()

const isActive = computed(() => {
  if (props.to === '/') {
    return route.path === '/'
  }
  return route.path.startsWith(props.to)
})
</script>
