<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <PageHeader :title="activeLabel" />

    <!-- Content: left nav + page -->
    <div class="grid grid-cols-1 md:grid-cols-[15rem_1fr] gap-6 items-start">
      <!-- Left Nav -->
      <nav class="bg-surface rounded-xl border border-border overflow-hidden" :aria-label="t('shell.settingsSections')">
        <div class="px-3 py-2 border-b border-border">
          <span class="text-micro font-semibold text-muted">{{ t('nav.settings') }}</span>
        </div>
        <div class="p-1.5 space-y-0.5">
          <router-link
            v-for="entry in entries"
            :key="entry.to"
            :to="entry.to"
            class="flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-foreground/70 hover:bg-surface-hover hover:text-foreground transition-colors"
            active-class="bg-primary-soft text-primary font-medium"
          >
            <component :is="entry.icon" class="w-4 h-4 shrink-0" aria-hidden="true" />
            {{ entry.label }}
          </router-link>
        </div>
      </nav>

      <!-- Page Content -->
      <div class="min-w-0">
        <router-view />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import PageHeader from '../../components/PageHeader.vue'
import { t } from '../../i18n/en'
import {
  UserCircleIcon,
  LockClosedIcon,
  KeyIcon,
} from '@heroicons/vue/24/outline'

const route = useRoute()

const entries = [
  { to: '/settings/profile',       label: t('settings.profile'),       icon: UserCircleIcon },
  { to: '/settings/password',      label: t('settings.password'),      icon: LockClosedIcon },
  { to: '/settings/tokens',        label: t('settings.tokens'),        icon: KeyIcon },
]

const activeLabel = computed(() => {
  const match = entries.find(e => route.path.startsWith(e.to))
  return match?.label ?? t('nav.settings')
})
</script>
