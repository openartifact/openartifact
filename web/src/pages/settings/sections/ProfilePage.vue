<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Avatar + Identity Card -->
    <div class="bg-surface rounded-xl border border-border p-6">
      <div class="flex items-center gap-5">
        <!-- Avatar -->
        <div
          class="w-16 h-16 rounded-full flex items-center justify-center bg-primary text-primary-contrast font-bold text-xl ring-2 ring-surface shrink-0"
         
        >
          {{ initials }}
        </div>
        <div class="min-w-0">
          <div class="text-lg font-bold tracking-tight">{{ session.user?.username }}</div>
          <div v-if="session.user?.full_name" class="text-sm text-muted mt-0.5">{{ session.user.full_name }}</div>
          <div class="flex items-center gap-2 mt-1.5">
            <span
              v-if="session.isAdmin"
              class="inline-flex items-center px-2 py-0.5 rounded-full text-caption font-medium bg-danger-soft/60 text-danger"
            >
              {{ t('settings.profile.admin') }}
            </span>
          </div>
        </div>
      </div>
    </div>

    <!-- Profile Fields -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-5 py-3.5 border-b border-border">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('settings.profile') }}</h3>
      </div>
      <div class="divide-y divide-border">
        <div class="flex items-center justify-between px-5 py-4">
          <div>
            <div class="text-caption font-semibold text-muted mb-0.5">{{ t('auth.username') }}</div>
            <div class="font-mono text-sm text-foreground">{{ session.user?.username }}</div>
          </div>
        </div>
        <div v-if="session.user?.email" class="flex items-center justify-between px-5 py-4">
          <div>
            <div class="text-caption font-semibold text-muted mb-0.5">{{ t('settings.profile.email') }}</div>
            <div class="text-sm text-foreground">{{ session.user.email }}</div>
          </div>
        </div>
        <div v-if="session.user?.full_name" class="flex items-center justify-between px-5 py-4">
          <div>
            <div class="text-caption font-semibold text-muted mb-0.5">{{ t('settings.profile.fullName') }}</div>
            <div class="text-sm text-foreground">{{ session.user.full_name }}</div>
          </div>
        </div>
        <div class="flex items-center justify-between px-5 py-4" data-testid="profile-groups">
          <div>
            <div class="text-caption font-semibold text-muted mb-0.5">{{ t('settings.profile.groups') }}</div>
            <div v-if="groups.length" class="flex flex-wrap gap-1.5">
              <span v-for="g in groups" :key="g" class="inline-flex items-center rounded-full bg-surface-hover px-2 py-0.5 font-mono text-caption text-foreground">{{ g }}</span>
            </div>
            <div v-else class="text-sm text-muted">{{ t('settings.profile.noGroups') }}</div>
          </div>
        </div>
        <div class="flex items-center justify-between px-5 py-4">
          <div>
            <div class="text-caption font-semibold text-muted mb-0.5">{{ t('settings.profile.role') }}</div>
            <div class="text-sm text-foreground">{{ session.isAdmin ? t('settings.profile.roleAdmin') : t('settings.profile.roleUser') }}</div>
          </div>
        </div>
      </div>
    </div>

    <!-- Credentials -->
    <section class="bg-surface rounded-xl border border-border overflow-hidden" data-testid="profile-credentials" aria-labelledby="profile-credentials-title">
      <div class="px-5 py-3.5 border-b border-border">
        <h3 id="profile-credentials-title" class="text-sm font-semibold tracking-tight">{{ t('sx.profile.credentials.title') }}</h3>
        <p class="text-sm text-muted">{{ t('sx.profile.credentials.desc') }}</p>
      </div>
      <ul class="divide-y divide-border">
        <li v-for="link in links" :key="link.to" class="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
          <div class="min-w-0">
            <div class="text-sm font-medium text-foreground">{{ link.label }}</div>
            <div class="text-sm text-muted">{{ link.hint }}</div>
          </div>
          <AppButton variant="secondary" size="sm" class="max-md:h-11" :to="link.to">{{ link.label }}</AppButton>
        </li>
      </ul>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import AppButton from '../../../components/AppButton.vue'
import { PASSWORD_PATH, PERSONAL_TOKENS_PATH } from '../../../lib/navigation'
import { useSessionStore } from '../../../stores/session'
import { t } from '../../../i18n'

const session = useSessionStore()

const links = [
  { to: PASSWORD_PATH, label: t('sx.profile.password'), hint: t('sx.profile.passwordHint') },
  { to: PERSONAL_TOKENS_PATH, label: t('sx.profile.tokens'), hint: t('sx.profile.tokensHint') },
]

const groups = computed(() => session.user?.groups ?? [])

const initials = computed(() => {
  const name = session.user?.full_name || session.user?.username || '?'
  return name
    .split(/[\s_.@-]/)
    .filter(Boolean)
    .slice(0, 2)
    .map((s: string) => s[0].toUpperCase())
    .join('')
})

</script>
