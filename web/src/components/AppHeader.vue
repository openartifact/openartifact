<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <header
    class="bg-surface border-b border-border relative flex items-center gap-2 px-4 md:px-8 py-3 shrink-0"
  >
    <!-- Mobile: opens the off-canvas navigation drawer -->
    <button
      type="button"
      class="md:hidden -ml-2 p-2 rounded-md text-foreground hover:bg-surface-hover transition-colors shrink-0"
      :aria-label="t('sidebar.open')"
      aria-controls="app-sidebar"
      :aria-expanded="ui.mobileNavOpen"
      @click="ui.toggleMobileNav()"
    >
      <Bars3Icon class="w-5 h-5" aria-hidden="true" />
    </button>

    <!-- Search trigger: pill centred on md+, fills the free space on mobile -->
    <button
      type="button"
      class="flex flex-1 min-w-0 items-center gap-2 px-4 py-1.5 rounded-full bg-surface-hover/60 border border-border hover:border-muted/50 text-left cursor-pointer transition-colors md:flex-none md:absolute md:left-1/2 md:-translate-x-1/2 md:w-64"
      :aria-label="t('shell.openPalette')"
      :aria-keyshortcuts="paletteHotkeyShortcut(apple)"
      @click="ui.openCommandPalette()"
    >
      <MagnifyingGlassIcon class="w-4 h-4 text-muted shrink-0" aria-hidden="true" />
      <span class="text-sm text-muted truncate">{{ t('shell.searchPlaceholder') }}</span>
      <kbd
        class="hidden sm:inline ml-auto text-micro font-mono text-muted bg-surface rounded px-1.5 py-0.5 border border-border leading-none"
        aria-hidden="true"
      >{{ paletteHotkeyLabel(apple) }}</kbd>
    </button>

    <!-- Right: actions (pushed to the right with ml-auto) -->
    <div class="md:ml-auto flex items-center gap-2 shrink-0">
      <ThemeToggle />

    </div>
  </header>
</template>

<script setup lang="ts">
import { Bars3Icon, MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import { useUIStore } from '../stores/ui'
import ThemeToggle from './ThemeToggle.vue'
import { isApplePlatform, paletteHotkeyLabel, paletteHotkeyShortcut } from '../lib/paletteHotkey'
import { t } from '../i18n/en'

const ui = useUIStore()
const apple = isApplePlatform()
</script>
