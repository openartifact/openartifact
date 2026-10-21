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
          class="w-16 h-16 rounded-full flex items-center justify-center text-white font-bold text-xl ring-2 ring-surface shrink-0"
          :style="avatarStyle"
        >
          {{ initials }}
        </div>
        <div class="min-w-0">
          <div class="text-lg font-bold tracking-tight">{{ session.user?.username }}</div>
          <div v-if="session.user?.full_name" class="text-sm text-muted mt-0.5">{{ session.user.full_name }}</div>
          <div class="flex items-center gap-2 mt-1.5">
            <span
              v-if="session.isAdmin"
              class="inline-flex items-center px-2 py-0.5 rounded-full text-[11px] font-medium bg-danger-soft/60 text-danger"
            >
              {{ t('settings.profile.admin') }}
            </span>
            <span class="inline-flex items-center px-2 py-0.5 rounded-full text-[11px] font-medium bg-surface-hover text-foreground/70">
              {{ t('auth.username').toLowerCase() }}: {{ session.user?.username }}
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
            <div class="text-[11px] font-semibold text-muted mb-0.5">{{ t('auth.username') }}</div>
            <div class="font-mono text-sm text-foreground">{{ session.user?.username }}</div>
          </div>
        </div>
        <div v-if="session.user?.email" class="flex items-center justify-between px-5 py-4">
          <div>
            <div class="text-[11px] font-semibold text-muted mb-0.5">{{ t('settings.profile.email') }}</div>
            <div class="text-sm text-foreground">{{ session.user.email }}</div>
          </div>
        </div>
        <div v-if="session.user?.full_name" class="flex items-center justify-between px-5 py-4">
          <div>
            <div class="text-[11px] font-semibold text-muted mb-0.5">{{ t('settings.profile.fullName') }}</div>
            <div class="text-sm text-foreground">{{ session.user.full_name }}</div>
          </div>
        </div>
        <div class="flex items-center justify-between px-5 py-4">
          <div>
            <div class="text-[11px] font-semibold text-muted mb-0.5">{{ t('settings.profile.role') }}</div>
            <div class="text-sm text-foreground">{{ session.isAdmin ? t('settings.profile.roleAdmin') : t('settings.profile.roleUser') }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useSessionStore } from '../../stores/session'
import { t } from '../../i18n/en'

const session = useSessionStore()

const initials = computed(() => {
  const name = session.user?.full_name || session.user?.username || '?'
  return name
    .split(/[\s_.@-]/)
    .filter(Boolean)
    .slice(0, 2)
    .map((s: string) => s[0].toUpperCase())
    .join('')
})

const avatarStyle = computed(() => {
  const name = session.user?.username || '?'
  const seed = name.charCodeAt(0) + (name.charCodeAt(1) || 0)
  const hues = [210, 280, 340, 25, 160, 195, 45]
  const hue = hues[seed % hues.length]
  return {
    background: `linear-gradient(135deg, hsl(${hue} 65% 50%), hsl(${hue + 30} 65% 40%))`,
  }
})
</script>
