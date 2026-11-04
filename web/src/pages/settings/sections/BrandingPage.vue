<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <SectionActions>
        <div class="flex items-center gap-2 shrink-0">
          <AppButton variant="danger-outline" size="sm" :disabled="loading || !admin || saving" data-testid="reset-all" @click="confirmReset = true">
            <ArrowUturnLeftIcon class="w-4 h-4" aria-hidden="true" />
            {{ t('adm.branding.resetAll') }}
          </AppButton>
          <AppButton size="sm" :loading="saving" :disabled="loading || !admin" data-testid="save" @click="save">
            <CheckIcon v-if="!saving" class="w-4 h-4" aria-hidden="true" />
            {{ saving ? t('common.saving') : t('adm.branding.save') }}
          </AppButton>
        </div>
      </SectionActions>

    <div v-if="loading" class="bg-surface rounded-xl border border-border p-12 flex flex-col items-center gap-3 text-muted" role="status">
      <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
      <span class="text-sm">{{ t('adm.branding.loading') }}</span>
    </div>

    <div v-else-if="loadError || !admin" class="bg-surface rounded-xl border border-border p-8 text-center text-danger" role="alert">
      {{ loadError || t('adm.branding.loadFailed') }}
    </div>

    <template v-else>
      <!-- Name and colour -->
      <section class="bg-surface rounded-xl border border-border p-5 space-y-5" aria-labelledby="branding-identity">
        <h2 id="branding-identity" class="text-sm font-semibold text-foreground">{{ t('adm.branding.section.identity') }}</h2>

        <div data-testid="field-product_name">
          <div class="mb-1.5 flex items-center justify-between gap-2">
            <label for="branding-name" class="block text-xs font-semibold text-foreground">{{ t('adm.branding.field.productName') }}</label>
            <BrandingFieldState :overridden="admin.overrides.product_name" :busy="busyField !== null" @reset="resetField('product_name')" />
          </div>
          <input
            id="branding-name"
            v-model="form.product_name"
            type="text"
            maxlength="64"
            autocomplete="off"
            :aria-invalid="!!errors.product_name"
            :aria-describedby="errors.product_name ? 'branding-name-error' : undefined"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm focus:ring-2 focus:ring-primary transition-colors"
          />
          <p v-if="errors.product_name" id="branding-name-error" role="alert" class="mt-1 text-xs text-danger">{{ t(errors.product_name) }}</p>
          <p v-else class="mt-1 text-caption text-muted">{{ t('adm.branding.field.productNameHint') }} {{ defaultNote('product_name') }}</p>
        </div>

        <div data-testid="field-primary_color">
          <div class="mb-1.5 flex items-center justify-between gap-2">
            <label for="branding-color" class="block text-xs font-semibold text-foreground">{{ t('adm.branding.field.color') }}</label>
            <BrandingFieldState :overridden="admin.overrides.primary_color" :busy="busyField !== null" @reset="resetField('primary_color')" />
          </div>
          <div class="flex items-center gap-2">
            <input
              type="color"
              :value="pickerValue"
              :aria-label="t('adm.branding.field.colorPicker')"
              class="h-9 w-12 shrink-0 cursor-pointer rounded-md border border-border-strong bg-bg p-1"
              data-testid="color-picker"
              @input="form.primary_color = ($event.target as HTMLInputElement).value"
            />
            <input
              id="branding-color"
              v-model="form.primary_color"
              type="text"
              maxlength="7"
              spellcheck="false"
              autocomplete="off"
              placeholder="#rrggbb"
              :aria-label="t('adm.branding.field.colorHex')"
              :aria-invalid="!!errors.primary_color"
              :aria-describedby="errors.primary_color ? 'branding-color-error' : undefined"
              class="w-32 px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
            />
          </div>
          <p v-if="errors.primary_color" id="branding-color-error" role="alert" class="mt-1 text-xs text-danger">{{ t(errors.primary_color) }}</p>
          <p v-else class="mt-1 text-caption text-muted">{{ t('adm.branding.field.colorHint') }} {{ defaultNote('primary_color') }}</p>

          <BrandingColorPreview :admin="admin" :color="form.primary_color" />
        </div>
      </section>

      <!-- Login message -->
      <section class="bg-surface rounded-xl border border-border p-5 space-y-3" aria-labelledby="branding-login">
        <h2 id="branding-login" class="text-sm font-semibold text-foreground">{{ t('adm.branding.section.login') }}</h2>
        <div data-testid="field-login_message">
          <div class="mb-1.5 flex items-center justify-between gap-2">
            <label for="branding-message" class="block text-xs font-semibold text-foreground">{{ t('adm.branding.field.message') }}</label>
            <BrandingFieldState :overridden="admin.overrides.login_message" :busy="busyField !== null" @reset="resetField('login_message')" />
          </div>
          <textarea
            id="branding-message"
            v-model="form.login_message"
            rows="5"
            :aria-invalid="!!errors.login_message"
            :aria-describedby="errors.login_message ? 'branding-message-error' : undefined"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary resize-y transition-colors"
          />
          <p class="mt-1 text-caption text-muted">
            {{ form.login_message.length }} / {{ LOGIN_MESSAGE_MAX }}
          </p>
          <p v-if="errors.login_message" id="branding-message-error" role="alert" class="mt-1 text-xs text-danger">{{ t(errors.login_message) }}</p>
          <p v-else class="mt-1 text-caption text-muted">{{ t('adm.branding.field.messageHint') }}</p>
        </div>
        <div class="rounded-lg border border-border bg-surface-hover/40 p-3 text-sm text-muted" data-testid="message-preview">
          <MiniMarkdown v-if="form.login_message.trim()" :source="form.login_message" />
          <p v-else>{{ t('adm.branding.preview.messageEmpty') }}</p>
        </div>
      </section>

      <!-- Links -->
      <section class="bg-surface rounded-xl border border-border p-5 space-y-5" aria-labelledby="branding-links">
        <h2 id="branding-links" class="text-sm font-semibold text-foreground">{{ t('adm.branding.section.links') }}</h2>

        <div data-testid="field-support_url">
          <div class="mb-1.5 flex items-center justify-between gap-2">
            <label for="branding-support" class="block text-xs font-semibold text-foreground">{{ t('adm.branding.field.support') }}</label>
            <BrandingFieldState :overridden="admin.overrides.support_url" :busy="busyField !== null" @reset="resetField('support_url')" />
          </div>
          <input
            id="branding-support"
            v-model="form.support_url"
            type="text"
            inputmode="url"
            autocomplete="off"
            placeholder="https://support.example.com"
            :aria-invalid="!!errors.support_url"
            :aria-describedby="errors.support_url ? 'branding-support-error' : undefined"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
          />
          <p v-if="errors.support_url" id="branding-support-error" role="alert" class="mt-1 text-xs text-danger">{{ t(errors.support_url) }}</p>
          <p v-else class="mt-1 text-caption text-muted">{{ t('adm.branding.field.supportHint') }} {{ defaultNote('support_url') }}</p>
        </div>

        <div data-testid="field-source_url">
          <div class="mb-1.5 flex items-center justify-between gap-2">
            <label for="branding-source" class="block text-xs font-semibold text-foreground">{{ t('adm.branding.field.source') }}</label>
            <BrandingFieldState :overridden="admin.overrides.source_url" :busy="busyField !== null" @reset="resetField('source_url')" />
          </div>
          <input
            id="branding-source"
            v-model="form.source_url"
            type="text"
            inputmode="url"
            autocomplete="off"
            :aria-invalid="!!errors.source_url"
            :aria-describedby="errors.source_url ? 'branding-source-error' : 'branding-source-hint'"
            class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono focus:ring-2 focus:ring-primary transition-colors"
          />
          <p v-if="errors.source_url" id="branding-source-error" role="alert" class="mt-1 text-xs text-danger">{{ t(errors.source_url) }}</p>
          <p id="branding-source-hint" class="mt-1 text-caption text-muted">{{ t('adm.branding.field.sourceHint') }} {{ defaultNote('source_url') }}</p>
        </div>
      </section>

      <BrandingImagesSection ref="images" :admin="admin" @update="adopt($event, [])" />
    </template>

    <ConfirmDialog
      :open="confirmReset"
      :title="t('adm.branding.resetAllTitle')"
      :description="t('adm.branding.resetAllBody')"
      :confirm-label="t('adm.branding.resetAll')"
      :busy="resetting"
      @confirm="resetAll"
      @close="confirmReset = false"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ArrowPathIcon, ArrowUturnLeftIcon, CheckIcon } from '@heroicons/vue/24/outline'
import SectionActions from '../../../components/SectionActions.vue'
import AppButton from '../../../components/AppButton.vue'
import ConfirmDialog from '../../../components/ConfirmDialog.vue'
import BrandingColorPreview from './BrandingColorPreview.vue'
import BrandingImagesSection from './BrandingImagesSection.vue'
import BrandingFieldState from '../../../components/BrandingFieldState.vue'
import MiniMarkdown from '../../../components/MiniMarkdown'
import {
  getAdminBranding,
  resetBranding,
  updateBranding,
  type BrandingAdmin,
} from '../../../api/branding'
import { useBrandingStore } from '../../../stores/branding'
import { useToastStore } from '../../../stores/toast'
import { isHexColor } from '../../../lib/branding'
import {
  BUILTIN_PRIMARY,
  LOGIN_MESSAGE_MAX,
  diffBranding,
  formFromAdmin,
  validateBrandingForm,
  type BrandingForm,
  type BrandingFormErrors,
  type BrandingFormField,
} from '../../../lib/brandingForm'
import { t, tp } from '../../../i18n'

const store = useBrandingStore()
const toasts = useToastStore()

const admin = ref<BrandingAdmin | null>(null)
const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const resetting = ref(false)
const confirmReset = ref(false)
const busyField = ref<BrandingFormField | null>(null)
const errors = ref<BrandingFormErrors>({})
const images = ref<InstanceType<typeof BrandingImagesSection> | null>(null)

const form = reactive<BrandingForm>({
  product_name: '',
  primary_color: '',
  login_message: '',
  support_url: '',
  source_url: '',
})

const pickerValue = computed(() => {
  const typed = form.primary_color.trim()
  if (isHexColor(typed)) return typed.toLowerCase()
  return admin.value?.effective.theme?.light.primary ?? BUILTIN_PRIMARY.light
})

/** Applies a server answer: the admin state, the live store, and (the given fields of) the form. */
function adopt(result: BrandingAdmin, fields?: readonly BrandingFormField[]): void {
  admin.value = result
  store.set(result.effective)
  const fresh = formFromAdmin(result)
  for (const field of fields ?? (Object.keys(fresh) as BrandingFormField[])) {
    form[field] = fresh[field]
    delete errors.value[field]
  }
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : ''
}

function defaultNote(field: BrandingFormField): string {
  const value = admin.value?.defaults[field] ?? ''
  const shown = value || t(field === 'primary_color' ? 'adm.branding.defaultBuiltin' : 'adm.branding.defaultNone')
  return tp('adm.branding.defaultValue', { value: shown })
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    adopt(await getAdminBranding())
  } catch (err) {
    loadError.value = message(err) || t('adm.branding.loadFailed')
  } finally {
    loading.value = false
  }
}

async function save(): Promise<void> {
  if (!admin.value) return
  errors.value = validateBrandingForm(form)
  if (Object.keys(errors.value).length > 0) {
    toasts.error(t('adm.branding.fixErrors'))
    return
  }
  const update = diffBranding(form, admin.value)
  if (Object.keys(update).length === 0) return
  saving.value = true
  try {
    adopt(await updateBranding(update))
    toasts.success(t('adm.branding.saved'))
  } catch (err) {
    toasts.error(t('adm.branding.saveFailed'), message(err))
  } finally {
    saving.value = false
  }
}

async function resetField(field: BrandingFormField): Promise<void> {
  busyField.value = field
  try {
    adopt(await updateBranding({ [field]: null }), [field])
  } catch (err) {
    toasts.error(t('adm.branding.resetFailed'), message(err))
  } finally {
    busyField.value = null
  }
}

async function resetAll(): Promise<void> {
  resetting.value = true
  try {
    adopt(await resetBranding())
    images.value?.clearErrors()
    confirmReset.value = false
    toasts.success(t('adm.branding.resetAllDone'))
  } catch (err) {
    toasts.error(t('adm.branding.resetFailed'), message(err))
  } finally {
    resetting.value = false
  }
}

onMounted(load)
</script>
