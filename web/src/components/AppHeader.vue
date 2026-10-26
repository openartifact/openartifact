<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <header class="relative flex shrink-0 items-center gap-2 border-b border-border bg-surface px-4 py-2 md:px-8">
    <!-- Mobile: opens the off-canvas navigation -->
    <button
      id="mobile-nav-toggle"
      type="button"
      class="-ml-2 flex h-11 w-11 shrink-0 items-center justify-center rounded-md text-foreground transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary md:hidden"
      :aria-label="t('sidebar.open')"
      aria-controls="app-sidebar"
      :aria-expanded="ui.mobileNavOpen"
      @click="ui.toggleMobileNav()"
    >
      <Bars3Icon class="h-5 w-5" aria-hidden="true" />
    </button>

    <!-- Mobile: the sidebar (and its lockup) is off-canvas, so the mark keeps the brand visible -->
    <router-link to="/" class="flex h-11 w-11 shrink-0 items-center justify-center rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary md:hidden" :aria-label="branding.productName">
      <BrandLogo variant="mark" :size="24" />
    </router-link>

    <!-- The page-level search: opens the command palette. No navigation is repeated here. -->
    <button
      type="button"
      class="flex min-h-11 min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-full border border-border bg-surface-hover/60 px-4 py-1.5 text-left transition-colors hover:border-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary md:min-h-9 md:max-w-md md:flex-none md:w-80 md:absolute md:left-1/2 md:-translate-x-1/2"
      aria-haspopup="dialog"
      :aria-label="t('shell.openPalette')"
      :aria-keyshortcuts="paletteHotkeyShortcut(apple)"
      data-testid="search-pill"
      @click="ui.openCommandPalette()"
    >
      <MagnifyingGlassIcon class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />
      <span class="truncate text-sm text-muted">{{ t('shell.searchPill') }}</span>
      <kbd
        class="ml-auto hidden rounded border border-border bg-surface px-1.5 py-0.5 font-mono text-micro leading-none text-muted sm:inline"
        aria-hidden="true"
      >{{ paletteHotkeyLabel(apple) }}</kbd>
    </button>

    <div class="flex shrink-0 items-center md:ml-auto">
      <UserMenu />
    </div>
  </header>
</template>

<script setup lang="ts">
import { Bars3Icon, MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import { useUIStore } from '../stores/ui'
import UserMenu from './UserMenu.vue'
import BrandLogo from './BrandLogo.vue'
import { useBrandingStore } from '../stores/branding'
import { isApplePlatform, paletteHotkeyLabel, paletteHotkeyShortcut } from '../lib/paletteHotkey'
import { t } from '../i18n/en'

const ui = useUIStore()
const branding = useBrandingStore()
const apple = isApplePlatform()
</script>
