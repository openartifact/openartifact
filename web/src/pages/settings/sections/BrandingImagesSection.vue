<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
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

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { ArrowUpTrayIcon, TrashIcon } from '@heroicons/vue/24/outline'
import AppButton from '../../../components/AppButton.vue'
import { deleteBrandingAsset, uploadBrandingAsset, type BrandingAdmin, type BrandingAsset } from '../../../api/branding'
import { useToastStore } from '../../../stores/toast'
import { validateImageFile } from '../../../lib/brandingForm'
import { t, tp, type TranslationKey } from '../../../i18n'

defineProps<{ admin: BrandingAdmin }>()
const emit = defineEmits<{ (e: 'update', result: BrandingAdmin): void }>()

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

const toasts = useToastStore()

const busyAsset = ref<BrandingAsset | null>(null)
const imageErrors = reactive<Partial<Record<BrandingAsset, TranslationKey>>>({})
const fileInputs: Partial<Record<BrandingAsset, HTMLInputElement | null>> = {}

function message(err: unknown): string {
  return err instanceof Error ? err.message : ''
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
    emit('update', await uploadBrandingAsset(asset, file))
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
    emit('update', await deleteBrandingAsset(asset))
    toasts.success(t('adm.branding.image.removed'))
  } catch (err) {
    toasts.error(t('adm.branding.image.removeFailed'), message(err))
  } finally {
    busyAsset.value = null
  }
}

function clearErrors(): void {
  for (const asset of Object.keys(imageErrors)) delete imageErrors[asset as BrandingAsset]
}

defineExpose({ clearErrors })
</script>
