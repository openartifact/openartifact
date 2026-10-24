<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="min-h-screen flex items-center justify-center bg-bg py-12 px-4">
    <div class="max-w-md w-full space-y-8">
      <div class="text-center">
        <div class="flex justify-center"><BrandLogo variant="stacked" :size="144" /></div>
        <h2 class="mt-2 text-xl text-foreground">{{ t('auth.changePassword') }}</h2>
        <p class="mt-2 text-sm text-muted">
          {{ t('auth.changePasswordRequired') }}
        </p>
      </div>
      <form class="mt-8 space-y-4" @submit.prevent="handleSubmit">
        <FormField :label="t('auth.currentPassword')" id="current_password" :error="errors.current_password">
          <input
            id="current_password"
            v-model="form.current_password"
            type="password"
            autocomplete="current-password"
            required
            class="w-full px-3 py-2 border border-border-strong rounded-md text-sm bg-surface text-foreground focus:outline-none focus:ring-2 focus:ring-primary"
          />
        </FormField>
        <FormField :label="t('auth.newPassword')" id="new_password" :error="errors.new_password">
          <input
            id="new_password"
            v-model="form.new_password"
            type="password"
            autocomplete="new-password"
            required
            minlength="12"
            class="w-full px-3 py-2 border border-border-strong rounded-md text-sm bg-surface text-foreground focus:outline-none focus:ring-2 focus:ring-primary"
          />
          <p class="mt-1 text-xs text-muted">{{ t('auth.passwordMin') }}</p>
        </FormField>
        <FormField :label="t('auth.confirmPassword')" id="confirm_password" :error="errors.confirm_password">
          <input
            id="confirm_password"
            v-model="form.confirm_password"
            type="password"
            autocomplete="new-password"
            required
            class="w-full px-3 py-2 border border-border-strong rounded-md text-sm bg-surface text-foreground focus:outline-none focus:ring-2 focus:ring-primary"
          />
        </FormField>
        <p v-if="errorMsg" role="alert" class="text-sm text-danger text-center">{{ errorMsg }}</p>
        <AppButton variant="primary" size="md" block type="submit" :disabled="loading">
          {{ loading ? t('common.saving') : t('auth.changePasswordCta') }}
        </AppButton>
        <AppButton variant="ghost" block @click="handleLogout">
          {{ t('auth.signOut') }}
        </AppButton>
      </form>
      <div class="flex justify-center"><PoweredBy /></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import AppButton from '../components/AppButton.vue'
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { changePassword } from '../api/auth'
import { useSessionStore } from '../stores/session'
import { useToastStore } from '../stores/toast'
import { useSignOut } from '../composables/useSignOut'
import { safeNextPath } from '../lib/safeRedirect'
import FormField from '../components/FormField.vue'
import BrandLogo from '../components/BrandLogo.vue'
import PoweredBy from '../components/PoweredBy.vue'
import { t } from '../i18n/en'

const router = useRouter()
const route = useRoute()
const session = useSessionStore()
const toasts = useToastStore()
const handleLogout = useSignOut()

const form = ref({ current_password: '', new_password: '', confirm_password: '' })
const errors = ref<Record<string, string>>({})
const loading = ref(false)
const errorMsg = ref('')

async function handleSubmit(): Promise<void> {
  errors.value = {}
  errorMsg.value = ''

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
    await session.fetchMe()
    if (!session.isAuthenticated || session.mustChangePassword) {
      // The password change itself succeeded, but we cannot continue into the app.
      session.clearSession()
      toasts.warning(t('auth.passwordNotApplied'))
      await router.replace({ name: 'login' })
      return
    }
    await router.replace(safeNextPath(route.query.next))
  } catch (err: unknown) {
    errorMsg.value = err instanceof Error ? err.message : t('auth.passwordChangeFailed')
  } finally {
    loading.value = false
  }
}
</script>
