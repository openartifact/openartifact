<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="fixed bottom-4 right-4 z-50 flex flex-col-reverse gap-2 pointer-events-none" role="region" aria-live="polite" :aria-label="t('shell.notifications')">
    <TransitionGroup
      enter-active-class="transition-all duration-300 ease-out"
      enter-from-class="opacity-0 translate-y-3"
      enter-to-class="opacity-100 translate-y-0"
      leave-active-class="transition-all duration-200 ease-in"
      leave-from-class="opacity-100 translate-y-0"
      leave-to-class="opacity-0 translate-y-3"
    >
      <div
        v-for="toast in toasts"
        :key="toast.id"
        class="pointer-events-auto w-80 rounded-lg border bg-surface shadow-lg overflow-hidden"
        :class="borderClass(toast.level)"
      >
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
              @click="runAction(toast)"
            >{{ toast.action.label }}</button>
          </div>
          <!-- Close -->
          <button
            type="button"
            :aria-label="t('shell.dismissToast')"
            @click="toastStore.dismiss(toast.id)"
            class="p-0.5 rounded text-muted hover:text-foreground transition-colors shrink-0"
          >
            <XMarkIcon class="w-4 h-4" aria-hidden="true" />
          </button>
        </div>
        <!-- Progress bar -->
        <div v-if="toast.timeoutMs > 0" class="h-0.5 w-full bg-border/30">
          <div
            class="h-full toast-progress"
            :class="progressClass(toast.level)"
            :style="{ '--duration': `${toast.timeoutMs || 4000}ms` }"
          ></div>
        </div>
      </div>
    </TransitionGroup>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { TransitionGroup } from 'vue'
import {
  CheckCircleIcon,
  InformationCircleIcon,
  ExclamationTriangleIcon,
  XCircleIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'
import { useToastStore, type Toast } from '../stores/toast'
import { t } from '../i18n/en'

const toastStore = useToastStore()
const toasts = computed(() => toastStore.visible)

function runAction(toast: Toast) {
  toastStore.dismiss(toast.id)
  toast.action?.run()
}

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
