<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The one user menu: who is signed in and with which role, the account pages,
  the theme, About, and Sign out. It lives in the header so every one of those
  stays reachable with the sidebar collapsed and on mobile.
-->
<template>
  <FloatingPopover
    id="user-menu-button"
    class="flex h-11 items-center gap-2 rounded-md px-1.5 text-foreground transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary md:h-9"
    :aria-label="tp('shell.userMenu', { name: username })"
    data-testid="user-menu"
    panel-class="w-64 rounded-md border border-border bg-surface shadow-lg"
    panel-testid="user-menu-panel"
  >
    <!-- The initial is drawn from an attribute: as text it would count as visible label text and differ from the account name. -->
    <span
      class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/20 text-sm font-medium text-primary before:content-[attr(data-initial)]"
      :data-initial="username[0]?.toUpperCase() ?? '?'"
      aria-hidden="true"
    />
    <span class="hidden max-w-32 truncate text-sm font-medium lg:inline">{{ username }}</span>
    <ChevronDownIcon class="hidden h-3.5 w-3.5 text-muted lg:block" aria-hidden="true" />

    <template #panel="{ close }">
      <div class="border-b border-border px-3 py-2.5">
        <p class="truncate text-sm font-semibold text-foreground" data-testid="user-menu-name">{{ username }}</p>
        <p class="truncate text-xs text-muted" data-testid="user-menu-role">{{ role }}</p>
      </div>

      <ul class="py-1">
        <li>
          <router-link :to="PROFILE_PATH" :class="ITEM" @click="close()">
            <UserCircleIcon class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />{{ t('shell.menu.profile') }}
          </router-link>
        </li>
        <li>
          <router-link :to="PERSONAL_TOKENS_PATH" :class="ITEM" @click="close()">
            <TicketIcon class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />{{ t('shell.menu.tokens') }}
          </router-link>
        </li>
      </ul>

      <div class="border-t border-border px-3 py-2">
        <p class="mb-1.5 text-xs text-muted">{{ t('shell.menu.theme') }}</p>
        <ThemeToggle />
      </div>

      <div class="border-t border-border px-3 py-2">
        <LanguageSelect id="language-select-menu" return-focus-to="user-menu-button" />
      </div>

      <ul class="border-t border-border py-1">
        <li v-if="canInstall">
          <button type="button" :class="ITEM" @click="onInstall(close)">
            <ArrowDownTrayIcon class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />{{ t('pwa.install') }}
          </button>
        </li>
        <li>
          <button type="button" :class="ITEM" data-testid="about-open" @click="openAbout(close)">
            <InformationCircleIcon class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />{{ t('shell.menu.about') }}
          </button>
        </li>
        <li>
          <button type="button" :class="ITEM" data-testid="sign-out" @click="onSignOut(close)">
            <ArrowRightOnRectangleIcon class="h-4 w-4 shrink-0 text-muted" aria-hidden="true" />{{ t('shell.menu.signOut') }}
          </button>
        </li>
      </ul>
    </template>
  </FloatingPopover>

  <AboutDialog :open="aboutOpen" @close="closeAbout" />
</template>

<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import {
  ArrowDownTrayIcon,
  ArrowRightOnRectangleIcon,
  ChevronDownIcon,
  InformationCircleIcon,
  TicketIcon,
  UserCircleIcon,
} from '@heroicons/vue/24/outline'
import FloatingPopover from './ui/FloatingPopover.vue'
import ThemeToggle from './ThemeToggle.vue'
import LanguageSelect from './LanguageSelect.vue'
import AboutDialog from './AboutDialog.vue'
import { useSessionStore } from '../stores/session'
import { useSignOut } from '../composables/useSignOut'
import { useInstallPrompt } from '../composables/useInstallPrompt'
import { PERSONAL_TOKENS_PATH, PROFILE_PATH } from '../lib/navigation'
import { t, tp } from '../i18n'

const ITEM =
  'flex min-h-11 w-full items-center gap-2.5 px-3 py-1.5 text-left text-sm text-foreground hover:bg-surface-hover focus-visible:bg-surface-hover focus-visible:outline-none md:min-h-9'

const session = useSessionStore()
const signOut = useSignOut()
const { canInstall, install } = useInstallPrompt()

const username = computed(() => session.user?.username ?? '')
const role = computed(() => (session.isAdmin ? t('shell.role.admin') : t('shell.role.member')))

const aboutOpen = ref(false)

function openAbout(close: () => void): void {
  close()
  aboutOpen.value = true
}

// The menu closes with its panel, so focus goes back to the menu button.
async function closeAbout(): Promise<void> {
  aboutOpen.value = false
  await nextTick()
  document.getElementById('user-menu-button')?.focus()
}

function onInstall(close: () => void): void {
  close()
  install()
}

function onSignOut(close: () => void): void {
  close()
  void signOut()
}
</script>
