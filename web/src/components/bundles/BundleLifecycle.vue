<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Where the bundle is in the release process: created, sealed, signed, then each stage of its
  pipeline. Every step says in words whether it is done, current or not reached.
-->
<template>
  <ol :aria-label="label" class="grid gap-2 sm:grid-cols-2 lg:grid-cols-[repeat(auto-fit,minmax(10rem,1fr))]" data-testid="lifecycle">
    <li
      v-for="step in steps"
      :key="step.id"
      class="min-w-0 rounded-lg border px-3 py-2"
      :class="CARD[step.status]"
      :aria-current="step.status === 'current' ? 'step' : undefined"
      :data-status="step.status"
    >
      <div class="flex items-center gap-1.5 text-sm font-medium">
        <component :is="ICON[step.status]" class="h-4 w-4 shrink-0" :class="ICON_CLASS[step.status]" aria-hidden="true" />
        <span class="min-w-0 truncate" :class="{ 'font-mono': step.kind === 'stage' }">{{ step.label }}</span>
      </div>
      <p class="mt-0.5 text-xs text-muted">
        <template v-if="step.status === 'done'">
          <RelativeTime :value="step.at" :fallback="t('bundles.step.done')" /><template v-if="step.by"> · {{ step.by }}</template>
        </template>
        <template v-else>{{ t(META[step.status]) }}</template>
      </p>
    </li>
  </ol>
</template>

<script setup lang="ts">
import { CheckCircleIcon, ClockIcon, MinusCircleIcon, PlayCircleIcon } from '@heroicons/vue/24/outline'
import RelativeTime from '../ui/RelativeTime.vue'
import type { LifecycleStep, StepStatus } from '../../lib/bundles'
import { t, type TranslationKey } from '../../i18n/en'

defineProps<{ steps: LifecycleStep[]; label: string }>()

const CARD: Record<StepStatus, string> = {
  done: 'border-border bg-surface text-foreground',
  current: 'border-primary bg-primary-soft text-foreground',
  todo: 'border-border bg-surface-hover/40 text-muted',
  skipped: 'border-border bg-surface-hover/40 text-muted',
}
const ICON = { done: CheckCircleIcon, current: PlayCircleIcon, todo: ClockIcon, skipped: MinusCircleIcon }
const ICON_CLASS: Record<StepStatus, string> = {
  done: 'text-success',
  current: 'text-primary',
  todo: 'text-muted',
  skipped: 'text-muted',
}
const META: Record<Exclude<StepStatus, 'done'>, TranslationKey> = {
  current: 'bundles.step.current',
  todo: 'bundles.step.todo',
  skipped: 'bundles.step.skipped',
}
</script>
