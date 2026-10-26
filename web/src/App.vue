<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <!-- The key re-creates the whole app when the language changes: setup code that read the catalog once picks up the new language. -->
  <div :key="locale" class="flex h-screen overflow-hidden bg-bg text-foreground">
    <a
      v-if="showShell"
      href="#main-content"
      class="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-[60] focus:rounded-md focus:bg-surface focus:px-3 focus:py-2 focus:text-sm focus:font-medium focus:text-foreground focus:shadow-lg focus:outline-none focus:ring-2 focus:ring-primary"
      data-testid="skip-link"
      @click.prevent="focusMain"
    >{{ t('shell.skipToContent') }}</a>

    <!-- Sidebar first (left); off-canvas menu below md -->
    <AppSidebar v-if="showShell" />
    <div
      v-if="showShell && ui.mobileNavOpen"
      class="fixed inset-0 z-30 bg-black/50 md:hidden"
      aria-hidden="true"
      @click="ui.closeMobileNav()"
    />

    <!-- While the off-canvas menu is open nothing behind it can take focus. -->
    <div class="flex min-w-0 flex-1 flex-col" :inert="showShell && isMobile && ui.mobileNavOpen">
      <AppHeader v-if="showShell" />
      <main
        id="main-content"
        class="nice-scroll flex-1 overflow-auto outline-none"
        tabindex="-1"
      >
        <div v-if="session.ready" class="mx-auto max-w-[1400px] px-4 py-5 md:px-8 md:py-7">
          <Suspense>
            <router-view :key="route.path" />
            <template #fallback>
              <PageLoadingFallback />
            </template>
          </Suspense>
        </div>
      </main>
      <!-- The attribution is fixed text: it never takes the white-label product name. -->
      <footer v-if="showShell" class="shrink-0 border-t border-border bg-surface px-4 py-2 md:px-8">
        <PoweredBy />
      </footer>
    </div>

    <ToastHost />
    <!-- The palette is a separate chunk, requested the first time it opens. -->
    <CommandPalette v-if="showShell && ui.commandPaletteLoaded" />
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useEventListener, useMediaQuery } from '@vueuse/core'
import AppHeader from './components/AppHeader.vue'
import AppSidebar from './components/AppSidebar.vue'
import PoweredBy from './components/PoweredBy.vue'
import ToastHost from './components/ToastHost.vue'
import PageLoadingFallback from './components/PageLoadingFallback.vue'
import { useUIStore } from './stores/ui'
import { useSessionStore } from './stores/session'
import { isPaletteHotkey, isPaletteSlashHotkey } from './lib/paletteHotkey'
import { loadRecent, pushRecent, recentFromPath, recentStorageKey, saveRecent } from './lib/paletteRecent'
import { currentLocale as locale, t } from './i18n'

const CommandPalette = defineAsyncComponent(() => import('./components/CommandPalette.vue'))

const route = useRoute()
const ui = useUIStore()
const session = useSessionStore()

const showShell = computed(() => route.meta.layout !== 'blank')
const isMobile = useMediaQuery('(max-width: 767px)')

function focusMain(): void {
  document.getElementById('main-content')?.focus()
}

// The off-canvas menu closes on navigation (the new page takes focus), on Escape
// and when the viewport grows past the mobile breakpoint.
watch(() => route.fullPath, () => ui.closeMobileNav(false))
watch(isMobile, (mobile) => {
  if (!mobile) ui.closeMobileNav(false)
})
useEventListener('keydown', (e: KeyboardEvent) => {
  if (e.key === 'Escape' && ui.mobileNavOpen && !ui.commandPaletteOpen) ui.closeMobileNav()
})

// Ctrl/Cmd+K toggles the palette and a bare "/" opens it (never while typing in
// a field or over another dialog), wherever the shell is mounted. The blank
// layout (login) keeps the keys for the browser.
useEventListener(
  'keydown',
  (e: KeyboardEvent) => {
    if (!showShell.value) return
    if (isPaletteHotkey(e)) {
      e.preventDefault()
      if (ui.commandPaletteOpen) ui.closeCommandPalette()
      else ui.openCommandPalette()
    } else if (
      !ui.commandPaletteOpen &&
      isPaletteSlashHotkey(e) &&
      !(e.target as Element | null)?.closest?.('[role="dialog"]')
    ) {
      e.preventDefault()
      ui.openCommandPalette()
    }
  },
  { passive: false },
)

// The palette's Recent list: what the signed-in user visited, kept per user.
watch(
  () => [route.path, session.user?.id, route.meta.layout] as const,
  ([path, userId, layout]) => {
    if (!userId || layout === 'blank') return
    const item = recentFromPath(path)
    if (!item) return
    const key = recentStorageKey(userId)
    saveRecent(window.localStorage, key, pushRecent(loadRecent(window.localStorage, key), item))
  },
  { immediate: true },
)
</script>
