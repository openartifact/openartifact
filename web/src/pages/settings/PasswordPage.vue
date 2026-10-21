<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Header card -->
    <div class="bg-surface rounded-xl border border-border p-6">
      <div class="text-lg font-bold tracking-tight">{{ t('settings.password') }}</div>
      <div class="text-sm text-muted mt-0.5">{{ t('settings.passwordDesc') }}</div>
    </div>

    <!-- Form card -->
    <div class="bg-surface rounded-xl border border-border overflow-hidden">
      <div class="px-5 py-3.5 border-b border-border">
        <h3 class="text-sm font-semibold tracking-tight">{{ t('auth.changePasswordCta') }}</h3>
      </div>
      <form @submit.prevent="handleChangePassword" class="divide-y divide-border">
        <!-- Current password -->
        <div class="flex items-start justify-between px-5 py-4 gap-6">
          <div class="min-w-[10rem]">
            <div class="text-[11px] font-semibold text-muted mb-0.5">
              {{ t('auth.currentPassword') }}
            </div>
          </div>
          <div class="flex-1">
            <input
              id="current_password"
              v-model="form.current_password"
              type="password"
              autocomplete="current-password"
              required
              class="w-full h-10 px-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/40 focus:border-primary"
            />
            <p v-if="errors.current_password" class="mt-1 text-xs text-danger">{{ errors.current_password }}</p>
          </div>
        </div>

        <!-- New password -->
        <div class="flex items-start justify-between px-5 py-4 gap-6">
          <div class="min-w-[10rem]">
            <div class="text-[11px] font-semibold text-muted mb-0.5">
              {{ t('auth.newPassword') }}
            </div>
            <div class="text-xs text-muted/70">{{ t('auth.passwordMin') }}</div>
          </div>
          <div class="flex-1">
            <input
              id="new_password"
              v-model="form.new_password"
              type="password"
              autocomplete="new-password"
              required
              minlength="12"
              class="w-full h-10 px-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/40 focus:border-primary"
            />
            <p v-if="errors.new_password" class="mt-1 text-xs text-danger">{{ errors.new_password }}</p>
          </div>
        </div>

        <!-- Confirm password -->
        <div class="flex items-start justify-between px-5 py-4 gap-6">
          <div class="min-w-[10rem]">
            <div class="text-[11px] font-semibold text-muted mb-0.5">
              {{ t('auth.confirmPassword') }}
            </div>
          </div>
          <div class="flex-1">
            <input
              id="confirm_password"
              v-model="form.confirm_password"
              type="password"
              autocomplete="new-password"
              required
              class="w-full h-10 px-3 rounded-lg bg-surface-hover/40 border border-border text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/40 focus:border-primary"
            />
            <p v-if="errors.confirm_password" class="mt-1 text-xs text-danger">{{ errors.confirm_password }}</p>
          </div>
        </div>

        <!-- Submit -->
        <div class="px-5 py-4 flex justify-end">
          <button
            type="submit"
            :disabled="loading"
            class="px-3 py-1.5 rounded-lg bg-primary text-primary-contrast text-sm font-medium hover:opacity-90 disabled:opacity-50 transition-opacity"
          >
            {{ loading ? t('common.saving') : t('auth.changePasswordCta') }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { changePassword } from '../../api/auth'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n/en'

const form = ref({ current_password: '', new_password: '', confirm_password: '' })
const errors = ref<Record<string, string>>({})
const loading = ref(false)
const toasts = useToastStore()

async function handleChangePassword(): Promise<void> {
  errors.value = {}
  if (form.value.new_password.length < 12) {
    errors.value.new_password = t('auth.passwordMinError')
    return
  }
  if (form.value.new_password !== form.value.confirm_password) {
    errors.value.confirm_password = t('auth.passwordMismatch')
    return
  }
  if (form.value.new_password === form.value.current_password) {
    errors.value.new_password = t('auth.passwordSame')
    return
  }
  loading.value = true
  try {
    await changePassword({
      current_password: form.value.current_password,
      new_password: form.value.new_password,
    })
    toasts.success(t('auth.changePasswordSuccess'))
    form.value = { current_password: '', new_password: '', confirm_password: '' }
  } catch (err: unknown) {
    toasts.error(err instanceof Error ? err.message : t('auth.passwordChangeFailed'))
  } finally {
    loading.value = false
  }
}
</script>
