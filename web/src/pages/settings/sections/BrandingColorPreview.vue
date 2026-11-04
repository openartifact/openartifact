<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
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
        :style="panelVars(color, admin, mode)"
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
</template>

<script setup lang="ts">
import type { BrandingAdmin } from '../../../api/branding'
import { panelVars } from '../../../lib/brandingForm'
import { t } from '../../../i18n'

const props = defineProps<{ admin: BrandingAdmin; color: string }>()

const THEMES = ['light', 'dark'] as const

function swatchesOf(mode: 'light' | 'dark') {
  const p = props.admin.effective.theme?.[mode]
  if (!p) return []
  return [
    { key: 'primary', label: 'adm.branding.swatch.primary', hex: p.primary },
    { key: 'contrast', label: 'adm.branding.swatch.contrast', hex: p.primary_contrast },
    { key: 'soft', label: 'adm.branding.swatch.soft', hex: p.primary_soft },
  ] as const
}
</script>
