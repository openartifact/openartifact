<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Fields of a cleanup policy (name, rule type and value, enabled). Shared by
  the repository cleanup tab and the project cleanup defaults; the parent owns
  the form object (see lib/cleanupPolicy.ts) and the surrounding dialog.
-->
<template>
  <div class="space-y-5">
    <div>
      <label for="cleanup-name" class="block text-xs font-semibold text-foreground mb-1.5">
        {{ t('cleanup.name') }} <span class="text-danger ml-0.5">*</span>
      </label>
      <input
        id="cleanup-name"
        v-model="form.name"
        type="text"
        :placeholder="t('cleanup.namePlaceholder')"
        class="w-full px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
      />
    </div>

    <div>
      <label class="block text-xs font-semibold text-foreground mb-2">
        {{ t('cleanup.type') }} <span class="text-danger ml-0.5">*</span>
      </label>
      <div class="grid grid-cols-2 gap-2">
        <button
          v-for="opt in POLICY_TYPES"
          :key="opt.value"
          type="button"
          :aria-pressed="form.type === opt.value"
          class="relative text-left p-3 rounded-lg border overflow-hidden transition-all"
          :class="
            form.type === opt.value
              ? 'border-primary bg-primary/5 ring-1 ring-primary/30'
              : 'border-border bg-surface hover:bg-surface-hover cursor-pointer'
          "
          @click="form.type = opt.value"
        >
          <span class="absolute left-0 top-0 bottom-0 w-[3px] rounded-l-lg" :style="{ background: opt.color }" />
          <div class="pl-3">
            <component :is="opt.icon" class="w-4 h-4 mb-1" :style="{ color: opt.color }" />
            <p class="text-xs font-semibold text-foreground leading-tight">{{ opt.label }}</p>
            <p class="text-caption text-muted leading-snug mt-0.5">{{ opt.desc }}</p>
          </div>
        </button>
      </div>
    </div>

    <div v-if="form.type === 'keep-last-n'">
      <label for="cleanup-keep-count" class="block text-xs font-semibold text-foreground mb-1.5">
        {{ t('cleanup.keepCount') }} <span class="text-danger ml-0.5">*</span>
      </label>
      <div class="flex items-center gap-2">
        <input
          id="cleanup-keep-count"
          v-model.number="form.keep_count"
          type="number"
          min="1"
          max="9999"
          placeholder="10"
          class="w-28 px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
        />
        <span class="text-sm text-muted">{{ t('cleanup.perPackage') }}</span>
      </div>
    </div>

    <div v-else>
      <label for="cleanup-keep-days" class="block text-xs font-semibold text-foreground mb-1.5">
        {{ t('cleanup.retention') }} <span class="text-danger ml-0.5">*</span>
      </label>
      <div class="flex items-center gap-2">
        <input
          id="cleanup-keep-days"
          v-model.number="form.keep_days"
          type="number"
          min="1"
          max="3650"
          placeholder="30"
          class="w-28 px-3 py-2 rounded-lg border border-border-strong bg-bg text-foreground text-sm font-mono placeholder:text-muted focus:ring-2 focus:ring-primary transition-colors"
        />
        <span class="text-sm text-muted">{{ t('cleanup.days') }}</span>
      </div>
    </div>

    <div class="flex items-start gap-3 p-3.5 rounded-lg border border-border bg-surface-hover/30">
      <AppToggle v-model="form.enabled" size="sm" labelledby="cleanup-enable-label" class="mt-0.5" />
      <div>
        <p id="cleanup-enable-label" class="text-xs font-semibold text-foreground">{{ t('cleanup.enable') }}</p>
        <p class="text-caption text-muted mt-0.5 leading-relaxed">{{ t('cleanup.enableHint') }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { CalendarIcon, QueueListIcon } from '@heroicons/vue/24/outline'
import AppToggle from './AppToggle.vue'
import type { CleanupPolicyForm } from '../lib/cleanupPolicy'
import { t } from '../i18n/en'

const form = defineModel<CleanupPolicyForm>({ required: true })

const POLICY_TYPES = [
  {
    value: 'keep-last-n',
    label: t('cleanup.type.lastN'),
    desc: t('cleanup.type.lastNDesc'),
    icon: QueueListIcon,
    color: 'rgb(var(--color-primary))',
  },
  {
    value: 'keep-by-age',
    label: t('cleanup.type.age'),
    desc: t('cleanup.type.ageDesc'),
    icon: CalendarIcon,
    color: 'rgb(var(--color-severity-medium))',
  },
] as const
</script>
