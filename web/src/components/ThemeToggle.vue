<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Light / dark / system, as a radio group (used inside the user menu). -->
<template>
  <div
    role="radiogroup"
    :aria-label="t('shell.menu.theme')"
    class="grid grid-cols-3 gap-1 rounded-md border border-border bg-surface-hover/60 p-0.5"
    data-testid="theme-toggle"
  >
    <button
      v-for="opt in OPTIONS"
      :key="opt.mode"
      type="button"
      role="radio"
      :aria-checked="theme.mode === opt.mode"
      class="flex min-h-11 items-center justify-center gap-1.5 rounded px-2 text-xs font-medium transition-colors md:min-h-8 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
      :class="theme.mode === opt.mode ? 'bg-surface text-foreground shadow-sm' : 'text-muted hover:text-foreground'"
      :data-testid="`theme-${opt.mode}`"
      @click="theme.setMode(opt.mode)"
    >
      <component :is="opt.icon" class="h-4 w-4 shrink-0" aria-hidden="true" />
      {{ t(opt.label) }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { ComputerDesktopIcon, MoonIcon, SunIcon } from '@heroicons/vue/24/outline'
import { useThemeStore } from '../stores/theme'
import { t } from '../i18n'

const theme = useThemeStore()

const OPTIONS = [
  { mode: 'light', label: 'theme.light', icon: SunIcon },
  { mode: 'dark', label: 'theme.dark', icon: MoonIcon },
  { mode: 'auto', label: 'theme.auto', icon: ComputerDesktopIcon },
] as const
</script>
