<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- One notification: icon, title, description, an optional action, a close button and, for a timed one, the countdown. -->
<template>
  <div class="pointer-events-auto w-full overflow-hidden rounded-lg border bg-surface shadow-lg md:w-80" :class="borderClass(toast.level)">
    <div class="flex items-start gap-3 px-4 py-3">
      <!-- Icon -->
      <component
        :is="iconFor(toast.level)"
        class="w-5 h-5 shrink-0 mt-0.5"
        :class="iconClass(toast.level)"
        aria-hidden="true"
      />
      <!-- Content -->
      <div class="flex-1 min-w-0">
        <p class="text-sm font-medium text-foreground">{{ toast.title }}</p>
        <p v-if="toast.description" class="text-xs text-muted mt-0.5">{{ toast.description }}</p>
        <button
          v-if="toast.action"
          type="button"
          class="mt-2 rounded-md bg-primary px-3 py-1 text-xs font-medium text-primary-contrast hover:opacity-90 transition-opacity"
          @click="emit('run')"
        >{{ toast.action.label }}</button>
      </div>
      <!-- Close -->
      <button
        type="button"
        :aria-label="t('shell.dismissToast')"
        @click="emit('dismiss')"
        class="p-0.5 rounded text-muted hover:text-foreground transition-colors shrink-0"
      >
        <XMarkIcon class="w-4 h-4" aria-hidden="true" />
      </button>
    </div>
    <!-- Progress bar -->
    <div v-if="toast.timeoutMs > 0" class="toast-timer h-0.5 w-full bg-border/30">
      <div
        class="h-full toast-progress"
        :class="progressClass(toast.level)"
        :style="{ '--duration': `${toast.timeoutMs || 4000}ms` }"
      ></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  CheckCircleIcon,
  InformationCircleIcon,
  ExclamationTriangleIcon,
  XCircleIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'
import type { Toast } from '../stores/toast'
import { t } from '../i18n'

defineProps<{ toast: Toast }>()
const emit = defineEmits<{ run: []; dismiss: [] }>()

function iconFor(level: string) {
  switch (level) {
    case 'success': return CheckCircleIcon
    case 'warning': return ExclamationTriangleIcon
    case 'error': return XCircleIcon
    default: return InformationCircleIcon
  }
}

function iconClass(level: string) {
  switch (level) {
    case 'success': return 'text-success'
    case 'warning': return 'text-warning'
    case 'error': return 'text-danger'
    default: return 'text-primary'
  }
}

function borderClass(level: string) {
  switch (level) {
    case 'success': return 'border-success/30'
    case 'warning': return 'border-warning/30'
    case 'error': return 'border-danger/30'
    default: return 'border-border'
  }
}

function progressClass(level: string) {
  switch (level) {
    case 'success': return 'bg-success'
    case 'warning': return 'bg-warning'
    case 'error': return 'bg-danger'
    default: return 'bg-primary'
  }
}
</script>

<style scoped>
.toast-progress {
  animation: shrink var(--duration) linear forwards;
}

@keyframes shrink {
  from {
    width: 100%;
  }
  to {
    width: 0%;
  }
}
</style>
