<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <DialogFrame :open="open" panel-class="w-full max-w-md rounded-xl border p-6 shadow-black/30" @close="close">
    <div class="flex items-start gap-4">
      <div
        class="w-10 h-10 rounded-full flex items-center justify-center shrink-0"
        :class="tone === 'danger' ? 'bg-danger-soft' : 'bg-primary-soft'"
      >
        <ExclamationTriangleIcon
          class="w-5 h-5"
          :class="tone === 'danger' ? 'text-danger' : 'text-primary'"
          aria-hidden="true"
        />
      </div>
      <div class="flex-1 min-w-0">
        <DialogTitle as="h2" class="text-base font-semibold text-foreground">{{ title }}</DialogTitle>
        <DialogDescription v-if="description" class="mt-1 text-sm text-muted">
          {{ description }}
        </DialogDescription>
      </div>
    </div>

    <div v-if="$slots.default" class="mt-4 text-sm">
      <slot />
    </div>

    <div class="flex justify-end gap-2 mt-6">
      <AppButton variant="secondary" :disabled="busy" @click="close">
        {{ cancelLabel ?? t('common.cancel') }}
      </AppButton>
      <AppButton
        :variant="tone === 'danger' ? 'danger' : 'primary'"
        :loading="busy"
        :disabled="confirmDisabled"
        @click="emit('confirm')"
      >
        {{ busy && busyLabel ? busyLabel : (confirmLabel ?? t('common.confirm')) }}
      </AppButton>
    </div>
  </DialogFrame>
</template>

<script setup lang="ts">
import { DialogDescription, DialogTitle } from '@headlessui/vue'
import { ExclamationTriangleIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import DialogFrame from './ui/DialogFrame.vue'
import { t } from '../i18n'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    description?: string
    confirmLabel?: string
    cancelLabel?: string
    /** Shown on the confirm button while `busy`. */
    busyLabel?: string
    tone?: 'danger' | 'primary'
    busy?: boolean
    confirmDisabled?: boolean
  }>(),
  {
    description: undefined,
    confirmLabel: undefined,
    cancelLabel: undefined,
    busyLabel: undefined,
    tone: 'danger',
    busy: false,
    confirmDisabled: false,
  },
)

const emit = defineEmits<{ (e: 'confirm'): void; (e: 'close'): void }>()

function close(): void {
  if (!props.busy) emit('close')
}
</script>
