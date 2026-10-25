<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div :class="field.half ? 'col-span-1' : 'col-span-2'">
    <!-- Toggle -->
    <template v-if="field.type === 'toggle'">
      <div class="flex items-center justify-between py-1.5">
        <div class="min-w-0 pr-3">
          <p class="text-xs font-semibold text-foreground">{{ field.label }}</p>
          <p v-if="field.hint" class="text-caption text-muted mt-0.5">{{ field.hint }}</p>
        </div>
        <AppToggle
          size="sm"
          :label="field.label"
          :model-value="!!config[field.key]"
          @update:model-value="emit('update', $event)"
        />
      </div>
    </template>

    <!-- Segmented control -->
    <template v-else-if="field.type === 'segmented'">
      <label class="block text-xs font-semibold text-foreground mb-1.5">{{ field.label }}</label>
      <div class="inline-flex p-0.5 rounded-lg bg-surface-hover border border-border-strong">
        <button
          v-for="opt in field.options"
          :key="opt.v"
          type="button"
          class="px-3 py-1 rounded-md text-xs font-medium transition-all"
          :class="
            (config[field.key] ?? field.default) === opt.v
              ? 'bg-surface text-foreground shadow-sm'
              : 'text-muted hover:text-foreground'
          "
          @click="emit('update', opt.v)"
        >
          {{ opt.label }}
        </button>
      </div>
    </template>

    <!-- Textarea -->
    <template v-else-if="field.type === 'textarea'">
      <label class="block text-xs font-semibold text-foreground mb-1.5">
        {{ field.label }}
        <span v-if="field.required" class="text-danger ml-0.5">*</span>
      </label>
      <textarea
        v-model="text"
        :placeholder="field.placeholder"
        :rows="field.rows ?? 4"
        class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm placeholder:text-muted focus:ring-2 focus:ring-primary resize-none transition-colors"
        :class="field.mono ? 'font-mono text-xs' : ''"
      />
      <p v-if="field.hint" class="text-caption text-muted mt-1">{{ field.hint }}</p>
    </template>

    <!-- Text / Secret -->
    <template v-else>
      <label class="block text-xs font-semibold text-foreground mb-1.5">
        {{ field.label }}
        <span v-if="field.required" class="text-danger ml-0.5">*</span>
      </label>
      <div class="relative">
        <input
          v-model="text"
          :type="field.type === 'secret' && !revealed ? 'password' : 'text'"
          :placeholder="field.type === 'secret' && config['has_' + field.key] ? t('adm.storage.secretStored') : field.placeholder"
          :aria-label="field.label"
          class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
          :class="[
            field.mono ? 'font-mono text-xs' : '',
            field.type === 'secret' ? 'pr-12' : '',
          ]"
        />
        <button
          v-if="field.type === 'secret'"
          type="button"
          :aria-label="tp('adm.storage.pair', { label: revealed ? t('adm.ai.hideKey') : t('adm.ai.showKey'), value: field.label })"
          :aria-pressed="revealed"
          class="absolute right-2.5 top-1/2 -translate-y-1/2 text-caption font-medium text-muted hover:text-foreground transition-colors"
          @click="emit('toggle-reveal')"
        >
          {{ revealed ? t('adm.ai.hide') : t('adm.ai.show') }}
        </button>
      </div>
      <p v-if="field.hint" class="text-caption text-muted mt-1">{{ field.hint }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import AppToggle from './AppToggle.vue'
import { t, tp } from '../i18n/en'
import type { FieldDef } from '../lib/storageBackends'

const props = defineProps<{
  field: FieldDef
  /** The backend's current config values (plus the server's `has_<field>` flags). */
  config: Record<string, unknown>
  /** Whether a secret field is shown in clear text. */
  revealed: boolean
}>()

const emit = defineEmits<{
  (e: 'update', value: unknown): void
  (e: 'toggle-reveal'): void
}>()

const text = computed<string>({
  get: () => props.config[props.field.key] as string,
  set: (value) => emit('update', value),
})
</script>
