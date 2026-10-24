<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="fixed inset-0 grid lg:grid-cols-[1.05fr_1fr] bg-bg overflow-hidden">

    <!-- ═══════════════════════════════════════════════
         LEFT — Brand panel (desktop only)
    ════════════════════════════════════════════════ -->
    <div
      class="relative hidden lg:flex flex-col justify-between p-10 overflow-hidden bg-surface border-r border-border"
    >

       <!-- Top — brand logo -->
       <div class="relative">
         <BrandLogo variant="stacked" :size="160" />
       </div>

      <!-- Middle — hero text -->
      <div class="relative">
        <p class="text-micro font-mono text-primary mb-3">
          {{ t('login.tagline') }}
        </p>
        <h1 class="text-4xl font-bold tracking-tight leading-[1.1] text-foreground">
          {{ t('login.heroTitleLine1') }}<br />{{ t('login.heroTitleLine2') }}
        </h1>
        <p class="mt-4 text-sm text-muted leading-relaxed max-w-sm">
          {{ t('login.heroBody') }}
        </p>
      </div>

      <!-- Bottom — supported formats -->
      <div class="relative flex flex-wrap gap-1.5">
        <FormatBadge v-for="fmt in ALL_FORMATS" :key="fmt" :format="fmt" />
      </div>
    </div>

    <!-- ═══════════════════════════════════════════════
         RIGHT — Form panel
    ════════════════════════════════════════════════ -->
    <div class="relative flex items-center justify-center p-6 sm:p-10">
      <div class="w-full max-w-sm">

         <!-- Mobile brand logo (hidden on lg+) -->
         <div class="lg:hidden flex justify-center mb-8">
           <BrandLogo variant="stacked" :size="144" />
        </div>

        <!-- Page heading -->
        <div class="mb-7">
          <h2 class="text-2xl font-bold tracking-tight text-foreground">{{ tp('login.headingProduct', { product: branding.productName }) }}</h2>
          <p class="mt-1 text-sm text-muted">{{ t('login.subheading') }}</p>
        </div>

        <!-- Operator message (markdown subset, rendered without HTML) -->
        <div
          v-if="branding.loginMessage.trim()"
          class="mb-5 rounded-md border border-border bg-surface-hover/40 p-3 text-sm text-muted"
          data-testid="login-message"
        >
          <MiniMarkdown :source="branding.loginMessage" />
        </div>

        <!-- Login form -->
        <form class="space-y-3.5" @submit.prevent="handleSubmit">

          <!-- Username -->
          <div>
            <label for="username" class="block text-caption font-semibold text-muted">
              {{ t('auth.username') }}
            </label>
            <input
              id="username"
              v-model="form.username"
              type="text"
              autocomplete="username"
              required
              class="mt-1.5 w-full h-10 px-3 rounded-md bg-surface-hover/40 border border-border-strong focus:bg-surface focus:border-primary focus:ring-2 focus:ring-primary/30 text-sm font-mono text-foreground transition-colors"
            />
          </div>

          <!-- Password -->
          <div>
            <div class="flex items-center justify-between">
              <label for="password" class="block text-caption font-semibold text-muted">
                {{ t('auth.password') }}
              </label>
            </div>
            <div class="relative mt-1.5">
              <input
                id="password"
                v-model="form.password"
                :type="showPassword ? 'text' : 'password'"
                autocomplete="current-password"
                required
                class="w-full h-10 pl-3 pr-10 rounded-md bg-surface-hover/40 border border-border-strong focus:bg-surface focus:border-primary focus:ring-2 focus:ring-primary/30 text-sm font-mono text-foreground transition-colors"
              />
              <button
                type="button"
                class="absolute right-2 top-1/2 -translate-y-1/2 p-1 rounded text-muted hover:text-foreground hover:bg-surface-hover transition-colors"
                :aria-label="showPassword ? t('login.hidePassword') : t('login.showPassword')"
                @click="showPassword = !showPassword"
              >
                <EyeSlashIcon v-if="showPassword" class="w-4 h-4" />
                <EyeIcon v-else class="w-4 h-4" />
              </button>
            </div>
          </div>

          <!-- Error message -->
          <div
            v-if="errorMsg"
            role="alert"
            class="flex items-start gap-2 p-2.5 rounded-md bg-danger/10 border border-danger/20 text-danger text-xs"
          >
            <XMarkIcon class="w-3.5 h-3.5 mt-0.5 shrink-0" />
            <span>{{ errorMsg }}</span>
          </div>

          <!-- Submit button -->
          <AppButton type="submit" :loading="loading" block>
            <template v-if="loading">{{ t('login.signingIn') }}</template>
            <template v-else>
              {{ t('auth.signIn') }}
              <ArrowRightIcon class="w-3.5 h-3.5" />
            </template>
          </AppButton>
        </form>

        <!-- SSO section -->
        <template v-if="ssoProviders.length > 0">
          <!-- Divider -->
          <div class="my-5 flex items-center gap-3">
            <span class="flex-1 h-px bg-border"></span>
            <span class="text-micro font-mono text-muted">{{ t('login.orContinueWith') }}</span>
            <span class="flex-1 h-px bg-border"></span>
          </div>

          <!-- SSO buttons -->
          <div class="grid gap-2">
            <a
              v-for="provider in ssoProviders"
              :key="provider.slug"
              :href="`/api/v1/auth/sso/${provider.kind}/${provider.slug}/login`"
              class="group h-10 rounded-md border border-border bg-surface hover:bg-surface-hover transition-colors flex items-center gap-2.5 px-3 text-sm"
            >
              <!-- Provider icon -->
              <svg
                v-if="ssoBrandIcon(provider.slug)"
                :viewBox="ssoBrandIcon(provider.slug)!.viewBox"
                class="w-6 h-6 shrink-0"
                aria-hidden="true"
                fill="currentColor"
                :style="{ color: providerColor(provider.slug) }"
              >
                <path :d="ssoBrandIcon(provider.slug)!.path" />
              </svg>
              <span
                v-else
                class="w-5 h-5 rounded-sm flex items-center justify-center text-white text-micro font-bold shrink-0"
                :style="{ backgroundColor: providerColor(provider.slug) }"
                aria-hidden="true"
              >
                {{ provider.display_name.charAt(0).toUpperCase() }}
              </span>
              <span class="font-medium text-foreground">{{ t('login.continueWith').replace('{name}', provider.display_name) }}</span>
              <span class="ml-auto text-micro font-mono text-muted">{{ provider.kind.toUpperCase() }}</span>
            </a>
          </div>
        </template>

        <!-- Attribution: always shown, independent of the branding -->
        <div class="mt-10">
          <PoweredBy />
        </div>
      </div>
    </div>

  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import {
  EyeIcon,
  EyeSlashIcon,
  XMarkIcon,
  ArrowRightIcon,
} from '@heroicons/vue/24/outline'
import { login, listSSOProviders } from '../api/auth'
import type { SSOProvider } from '../api/auth'
import { useSessionStore } from '../stores/session'
import FormatBadge from '../components/FormatBadge.vue'
import { t, tp } from '../i18n/en'
import { ssoBrandIcon } from '../lib/brandIcons'
import { ALL_FORMATS } from '../lib/formats'
import { useBrandingStore } from '../stores/branding'
import { ssoProviderColor } from '../lib/brandColors'
import { safeNextPath } from '../lib/safeRedirect'
import AppButton from '../components/AppButton.vue'
import BrandLogo from '../components/BrandLogo.vue'
import MiniMarkdown from '../components/MiniMarkdown'
import PoweredBy from '../components/PoweredBy.vue'

const router = useRouter()
const route = useRoute()
const session = useSessionStore()
const branding = useBrandingStore()

const form = ref({ username: '', password: '' })
const loading = ref(false)
const errorMsg = ref('')
const showPassword = ref(false)
const ssoProviders = ref<SSOProvider[]>([])

const providerColor = ssoProviderColor

onMounted(async () => {
  try {
    ssoProviders.value = await listSSOProviders()
  } catch {
    errorMsg.value = t('login.ssoLoadFailed')
  }
})

async function handleSubmit(): Promise<void> {
  errorMsg.value = ''
  loading.value = true
  try {
    const user = await login(form.value)
    session.user = user
    // Fetch full profile (includes is_admin) before navigating so the
    // sidebar admin section is visible immediately without a reload.
    await session.fetchMe()
    if (session.mustChangePassword) {
      await router.push({ name: 'change-password' })
      return
    }
    await router.push(safeNextPath(route.query.next))
  } catch (err: unknown) {
    errorMsg.value = err instanceof Error ? err.message : t('auth.loginFailed')
  } finally {
    loading.value = false
  }
}
</script>
