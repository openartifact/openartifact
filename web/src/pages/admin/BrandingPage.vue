<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <PageHeader :title="t('adm.branding.title')" :description="t('adm.branding.description')">
      <template #actions>
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
      </template>
    </PageHeader>

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

          <!-- Saved, contrast-adjusted colours -->
          <div class="mt-3" data-testid="swatches">
            <p class="text-caption font-semibold text-muted">{{ t('adm.branding.swatch.title') }}</p>
            <p v-if="!admin.effective.theme" class="mt-1 text-caption text-muted">{{ t('adm.branding.swatch.none') }}</p>
            <div v-else class="mt-1.5 grid gap-3 sm:grid-cols-2">
              <div v-for="mode in THEMES" :key="mode" :data-testid="`swatch-${mode}`">
                <p class="text-caption text-muted">{{ t(`adm.branding.preview.${mode}`) }}</p>
                <ul class="mt-1 flex flex-wrap gap-3">
                  <li v-for="swatch in swatchesOf(mode)" :key="swatch.key" class="flex items-center gap-1.5 text-caption text-muted">
                    <span class="h-5 w-5 rounded border border-border-strong" :style="{ backgroundColor: swatch.hex }" aria-hidden="true" />
                    <span>{{ t(swatch.label) }} <span class="font-mono text-foreground">{{ swatch.hex }}</span></span>
                  </li>
                </ul>
              </div>
            </div>
          </div>

          <!-- Live preview in both themes -->
          <div class="mt-4" data-testid="color-preview">
            <p class="text-caption font-semibold text-muted">{{ t('adm.branding.preview.title') }}</p>
            <p class="text-caption text-muted">{{ t('adm.branding.preview.approx') }}</p>
            <div class="mt-1.5 grid gap-3 sm:grid-cols-2">
              <div
                v-for="mode in THEMES"
                :key="mode"
                class="rounded-lg border p-3"
                :class="mode === 'dark' ? 'dark bg-surface text-foreground border-border' : 'bg-white text-gray-900 border-gray-200'"
                :style="panelVars(form.primary_color, admin, mode)"
                :data-testid="`preview-${mode}`"
              >
                <p class="mb-2 text-caption" :class="mode === 'dark' ? 'text-muted' : 'text-gray-500'">{{ t(`adm.branding.preview.${mode}`) }}</p>
                <div class="flex flex-wrap items-center gap-3">
                  <span class="inline-flex h-8 items-center rounded-lg bg-primary px-3 text-sm font-medium text-primary-contrast">{{ t('adm.branding.preview.button') }}</span>
                  <span class="text-sm font-medium text-primary underline underline-offset-2">{{ t('adm.branding.preview.link') }}</span>
                  <span class="rounded-full bg-primary-soft px-2 py-0.5 text-caption font-medium text-primary">{{ t('adm.branding.preview.chip') }}</span>
                </div>
              </div>
            </div>
          </div>
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

      <!-- Images -->
      <section class="bg-surface rounded-xl border border-border p-5 space-y-4" aria-labelledby="branding-images">
        <div>
          <h2 id="branding-images" class="text-sm font-semibold text-foreground">{{ t('adm.branding.section.images') }}</h2>
          <p class="mt-0.5 text-caption text-muted">{{ t('adm.branding.image.formats') }}</p>
        </div>
        <div class="grid gap-4 md:grid-cols-3">
          <div
            v-for="slot in IMAGE_SLOTS"
            :key="slot.asset"
            class="flex flex-col gap-2 rounded-lg border border-border p-3"
            :data-testid="`image-${slot.asset}`"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="text-xs font-semibold text-foreground">{{ t(slot.label) }}</span>
              <span
                class="rounded-full px-2 py-0.5 text-caption font-medium"
                :class="admin.overrides[slot.override] ? 'bg-primary-soft text-primary' : 'bg-surface-hover text-muted'"
              >{{ admin.overrides[slot.override] ? t('adm.branding.state.overridden') : t('adm.branding.state.default') }}</span>
            </div>
            <div
              class="flex h-20 items-center justify-center rounded-md border p-2"
              :class="slot.dark ? 'dark bg-surface border-border text-muted' : 'bg-white border-gray-200 text-gray-500'"
            >
              <img
                v-if="admin.overrides[slot.override] && admin.effective[slot.url]"
                :src="admin.effective[slot.url]"
                :alt="tp('adm.branding.image.alt', { slot: t(slot.label) })"
                class="max-h-full max-w-full object-contain"
                :class="slot.asset === 'favicon' && 'h-8 w-8'"
                data-testid="image-preview"
              />
              <span v-else class="text-center text-caption">{{ t('adm.branding.image.notSet') }}</span>
            </div>
            <p class="text-caption text-muted">{{ t(slot.hint) }}</p>
            <div class="mt-auto flex items-center gap-2">
              <AppButton variant="secondary" size="xs" :loading="busyAsset === slot.asset" :disabled="busyAsset !== null" @click="openPicker(slot.asset)">
                <ArrowUpTrayIcon class="w-3.5 h-3.5" aria-hidden="true" />
                {{ admin.overrides[slot.override] ? t('adm.branding.image.replace') : t('adm.branding.image.upload') }}
              </AppButton>
              <AppButton
                v-if="admin.overrides[slot.override]"
                variant="ghost"
                size="xs"
                :disabled="busyAsset !== null"
                data-testid="image-remove"
                @click="removeAsset(slot.asset)"
              >
                <TrashIcon class="w-3.5 h-3.5" aria-hidden="true" />
                {{ t('adm.branding.image.remove') }}
              </AppButton>
              <input
                :ref="(el) => (fileInputs[slot.asset] = el as HTMLInputElement | null)"
                type="file"
                accept="image/png,image/svg+xml"
                class="hidden"
                :data-testid="`file-${slot.asset}`"
                @change="onFile(slot.asset, $event)"
              />
            </div>
            <p v-if="imageErrors[slot.asset]" role="alert" class="text-xs text-danger" data-testid="image-error">{{ t(imageErrors[slot.asset]!) }}</p>
          </div>
        </div>
      </section>
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
import { ArrowPathIcon, ArrowUpTrayIcon, ArrowUturnLeftIcon, CheckIcon, TrashIcon } from '@heroicons/vue/24/outline'
import PageHeader from '../../components/PageHeader.vue'
import AppButton from '../../components/AppButton.vue'
import ConfirmDialog from '../../components/ConfirmDialog.vue'
import BrandingFieldState from '../../components/BrandingFieldState.vue'
import MiniMarkdown from '../../components/MiniMarkdown'
import {
  deleteBrandingAsset,
  getAdminBranding,
  resetBranding,
  updateBranding,
  uploadBrandingAsset,
  type BrandingAdmin,
  type BrandingAsset,
} from '../../api/branding'
import { useBrandingStore } from '../../stores/branding'
import { useToastStore } from '../../stores/toast'
import { isHexColor } from '../../lib/branding'
import {
  BUILTIN_PRIMARY,
  LOGIN_MESSAGE_MAX,
  diffBranding,
  formFromAdmin,
  panelVars,
  validateBrandingForm,
  validateImageFile,
  type BrandingForm,
  type BrandingFormErrors,
  type BrandingFormField,
} from '../../lib/brandingForm'
import { t, tp, type TranslationKey } from '../../i18n/en'

const THEMES = ['light', 'dark'] as const

const IMAGE_SLOTS = [
  { asset: 'logo-light', override: 'logo_light', url: 'logo_light_url', label: 'adm.branding.image.logoLight', hint: 'adm.branding.image.logoLightHint', dark: false },
  { asset: 'logo-dark', override: 'logo_dark', url: 'logo_dark_url', label: 'adm.branding.image.logoDark', hint: 'adm.branding.image.logoDarkHint', dark: true },
  { asset: 'favicon', override: 'favicon', url: 'favicon_url', label: 'adm.branding.image.favicon', hint: 'adm.branding.image.faviconHint', dark: false },
] as const satisfies readonly {
  asset: BrandingAsset
  override: 'logo_light' | 'logo_dark' | 'favicon'
  url: 'logo_light_url' | 'logo_dark_url' | 'favicon_url'
  label: TranslationKey
  hint: TranslationKey
  dark: boolean
}[]

const store = useBrandingStore()
const toasts = useToastStore()

const admin = ref<BrandingAdmin | null>(null)
const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const resetting = ref(false)
const confirmReset = ref(false)
const busyField = ref<BrandingFormField | null>(null)
const busyAsset = ref<BrandingAsset | null>(null)
const errors = ref<BrandingFormErrors>({})
const imageErrors = reactive<Partial<Record<BrandingAsset, TranslationKey>>>({})
const fileInputs: Partial<Record<BrandingAsset, HTMLInputElement | null>> = {}

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

function swatchesOf(mode: 'light' | 'dark') {
  const p = admin.value?.effective.theme?.[mode]
  if (!p) return []
  return [
    { key: 'primary', label: 'adm.branding.swatch.primary', hex: p.primary },
    { key: 'contrast', label: 'adm.branding.swatch.contrast', hex: p.primary_contrast },
    { key: 'soft', label: 'adm.branding.swatch.soft', hex: p.primary_soft },
  ] as const
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
    for (const asset of Object.keys(imageErrors)) delete imageErrors[asset as BrandingAsset]
    confirmReset.value = false
    toasts.success(t('adm.branding.resetAllDone'))
  } catch (err) {
    toasts.error(t('adm.branding.resetFailed'), message(err))
  } finally {
    resetting.value = false
  }
}

function openPicker(asset: BrandingAsset): void {
  fileInputs[asset]?.click()
}

async function onFile(asset: BrandingAsset, event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  const problem = validateImageFile(file)
  if (problem) {
    imageErrors[asset] = problem
    return
  }
  delete imageErrors[asset]
  busyAsset.value = asset
  try {
    adopt(await uploadBrandingAsset(asset, file), [])
    toasts.success(t('adm.branding.image.uploaded'))
  } catch (err) {
    toasts.error(t('adm.branding.image.uploadFailed'), message(err))
  } finally {
    busyAsset.value = null
  }
}

async function removeAsset(asset: BrandingAsset): Promise<void> {
  busyAsset.value = asset
  delete imageErrors[asset]
  try {
    adopt(await deleteBrandingAsset(asset), [])
    toasts.success(t('adm.branding.image.removed'))
  } catch (err) {
    toasts.error(t('adm.branding.image.removeFailed'), message(err))
  } finally {
    busyAsset.value = null
  }
}

onMounted(load)
</script>
