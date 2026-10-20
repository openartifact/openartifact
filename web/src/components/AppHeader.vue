<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <header
    class="bg-surface/80 backdrop-blur-sm border-b border-border relative flex items-center px-8 py-3 shrink-0"
  >
    <!-- Center: pill search trigger (absolutely centered) -->
    <div
      class="absolute left-1/2 -translate-x-1/2 flex items-center gap-2 px-4 py-1.5 rounded-full bg-surface-hover/60 border border-border hover:border-muted/50 cursor-pointer transition-colors w-64"
      role="button"
      :aria-label="t('shell.openPalette')"
      tabindex="0"
      @click="ui.openCommandPalette()"
      @keydown.enter.prevent="ui.openCommandPalette()"
      @keydown.space.prevent="ui.openCommandPalette()"
    >
      <MagnifyingGlassIcon class="w-4 h-4 text-muted shrink-0" aria-hidden="true" />
      <span class="text-sm text-muted">{{ t('shell.searchPlaceholder') }}</span>
      <kbd
        class="ml-auto text-[10px] font-mono text-muted bg-surface rounded px-1.5 py-0.5 border border-border leading-none"
        aria-hidden="true"
      >⌘K</kbd>
    </div>

    <!-- Right: actions (pushed to the right with ml-auto) -->
    <div class="ml-auto flex items-center gap-2">
      <ThemeToggle />

    </div>
  </header>
</template>

<script setup lang="ts">
import { MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import { useUIStore } from '../stores/ui'
import ThemeToggle from './ThemeToggle.vue'
import { t } from '../i18n/en'

const ui = useUIStore()
</script>
