<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="bg-surface rounded-xl border border-border p-5 space-y-3">
    <div>
      <h2 class="text-sm font-semibold text-foreground">{{ t('adm.ai.provider') }}</h2>
      <p class="text-caption text-muted mt-0.5">{{ t('adm.ai.providerHint') }}</p>
    </div>
    <div class="grid grid-cols-2 gap-2" role="radiogroup" :aria-label="t('adm.ai.provider')">
      <button
        v-for="(meta, key) in providers"
        :key="key"
        type="button"
        role="radio"
        :aria-checked="selected === key"
        :tabindex="enabled ? 0 : -1"
        class="relative text-left p-3 rounded-lg border overflow-hidden transition-all focus-visible:ring-2 focus-visible:ring-primary"
        :class="selected === key
          ? 'border-primary bg-primary-soft/40 ring-1 ring-primary/30'
          : 'border-border bg-surface hover:bg-surface-hover cursor-pointer'"
        @click="emit('select', String(key))"
      >
        <span class="absolute left-0 top-0 bottom-0 w-[3px] rounded-l-lg" :style="{ background: aiProviderColor(String(key)) }" />
        <div class="pl-3 flex items-start gap-2.5">
          <div class="w-7 h-7 rounded-md flex items-center justify-center shrink-0 mt-0.5" :style="{ background: tint(aiProviderColor(String(key)), 12) }">
            <svg
              v-if="aiBrandIcon(String(key))"
              class="w-4 h-4"
              :style="{ color: aiProviderColor(String(key)) }"
              aria-hidden="true"
              :viewBox="aiBrandIcon(String(key))!.viewBox"
              fill="currentColor"
            >
              <path :d="aiBrandIcon(String(key))!.path" />
            </svg>
            <component v-else :is="meta.icon" class="w-4 h-4" :style="{ color: aiProviderColor(String(key)) }" aria-hidden="true" />
          </div>
          <div class="min-w-0">
            <p class="text-xs font-semibold text-foreground leading-tight">{{ meta.label }}</p>
            <p class="text-caption text-muted leading-snug mt-0.5">{{ meta.desc }}</p>
          </div>
        </div>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { ProviderMeta } from '../../../lib/aiProviders'
import { aiProviderColor, tint } from '../../../lib/brandColors'
import { aiBrandIcon } from '../../../lib/brandIcons'
import { t } from '../../../i18n'

defineProps<{
  providers: Record<string, ProviderMeta>
  selected: string
  enabled: boolean
}>()
const emit = defineEmits<{ (e: 'select', key: string): void }>()
</script>
