<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="h-screen bg-bg text-foreground flex overflow-hidden">
    <!-- Sidebar first (left); off-canvas drawer below md -->
    <AppSidebar v-if="showShell" />
    <div
      v-if="showShell && ui.mobileNavOpen"
      class="fixed inset-0 z-30 bg-black/50 md:hidden"
      aria-hidden="true"
      @click="ui.closeMobileNav()"
    />

    <!-- Main area: header + scrollable content -->
    <div class="flex-1 flex flex-col min-w-0">
      <AppHeader v-if="showShell" />
      <main
        class="flex-1 overflow-auto nice-scroll"
        id="main-content"
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
    </div>

    <ToastHost />
    <CommandPalette v-if="showShell" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useEventListener } from '@vueuse/core'
import AppHeader from './components/AppHeader.vue'
import AppSidebar from './components/AppSidebar.vue'
import ToastHost from './components/ToastHost.vue'
import CommandPalette from './components/CommandPalette.vue'
import PageLoadingFallback from './components/PageLoadingFallback.vue'
import { useUIStore } from './stores/ui'
import { useThemeStore } from './stores/theme'
import { useSessionStore } from './stores/session'
import { registerCommand, unregisterCommand } from './composables/useCommands'
import { useSignOut } from './composables/useSignOut'
import { isPaletteHotkey } from './lib/paletteHotkey'
import { t } from './i18n/en'

const route = useRoute()
const router = useRouter()
const ui = useUIStore()
const theme = useThemeStore()
const session = useSessionStore()
const signOut = useSignOut()

const showShell = computed(() => route.meta.layout !== 'blank')

// The mobile drawer closes on navigation and on Escape.
watch(() => route.fullPath, () => ui.closeMobileNav())
useEventListener('keydown', (e: KeyboardEvent) => {
  if (e.key === 'Escape' && ui.mobileNavOpen) ui.closeMobileNav()
})

// Ctrl/Cmd+K toggles the command palette wherever it is mounted (not on the
// blank layout, where the key stays the browser's). Esc is handled by the
// Headless UI Dialog itself.
useEventListener(
  'keydown',
  (e: KeyboardEvent) => {
    if (showShell.value && isPaletteHotkey(e)) {
      e.preventDefault()
      if (ui.commandPaletteOpen) ui.closeCommandPalette()
      else ui.openCommandPalette()
    }
  },
  { passive: false },
)

const COMMAND_IDS = [
  'nav.dashboard',
  'nav.repos',
  'nav.search',
  'nav.settings',
  'theme.light',
  'theme.dark',
  'theme.auto',
  'auth.logout',
]

onMounted(() => {
  registerCommand({
    id: 'nav.dashboard',
    title: t('palette.command.dashboard'),
    group: 'commands',
    run: () => { router.push('/') },
  })
  registerCommand({
    id: 'nav.repos',
    title: t('palette.command.repos'),
    group: 'commands',
    run: () => { router.push('/repos') },
  })
  registerCommand({
    id: 'nav.search',
    title: t('palette.command.search'),
    group: 'commands',
    run: () => { router.push('/search') },
  })
  registerCommand({
    id: 'nav.settings',
    title: t('palette.command.settings'),
    group: 'commands',
    run: () => { router.push('/settings/profile') },
  })
  registerCommand({
    id: 'theme.light',
    title: t('palette.command.theme.light'),
    group: 'commands',
    run: () => theme.setMode('light'),
  })
  registerCommand({
    id: 'theme.dark',
    title: t('palette.command.theme.dark'),
    group: 'commands',
    run: () => theme.setMode('dark'),
  })
  registerCommand({
    id: 'theme.auto',
    title: t('palette.command.theme.auto'),
    group: 'commands',
    run: () => theme.setMode('auto'),
  })
  registerCommand({
    id: 'auth.logout',
    title: t('palette.command.logout'),
    group: 'commands',
    run: signOut,
  })
})

onBeforeUnmount(() => {
  for (const id of COMMAND_IDS) unregisterCommand(id)
})
</script>
