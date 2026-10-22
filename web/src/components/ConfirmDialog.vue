<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <TransitionRoot appear :show="open" as="template">
    <Dialog as="div" class="relative z-50" @close="close">
      <TransitionChild
        as="template"
        enter="motion-safe:duration-150 motion-safe:ease-out"
        enter-from="opacity-0"
        enter-to="opacity-100"
        leave="motion-safe:duration-100 motion-safe:ease-in"
        leave-from="opacity-100"
        leave-to="opacity-0"
      >
        <div class="fixed inset-0 bg-black/50" aria-hidden="true" />
      </TransitionChild>

      <div class="fixed inset-0 overflow-y-auto">
        <div class="flex min-h-full items-center justify-center p-4">
          <TransitionChild
            as="template"
            enter="motion-safe:duration-200 motion-safe:ease-out"
            enter-from="opacity-0 scale-95"
            enter-to="opacity-100 scale-100"
            leave="motion-safe:duration-150 motion-safe:ease-in"
            leave-from="opacity-100 scale-100"
            leave-to="opacity-0 scale-95"
          >
            <DialogPanel
              class="w-full max-w-md rounded-xl border border-border bg-surface p-6 shadow-2xl shadow-black/30"
            >
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
            </DialogPanel>
          </TransitionChild>
        </div>
      </div>
    </Dialog>
  </TransitionRoot>
</template>

<script setup lang="ts">
import {
  Dialog,
  DialogPanel,
  DialogTitle,
  DialogDescription,
  TransitionRoot,
  TransitionChild,
} from '@headlessui/vue'
import { ExclamationTriangleIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import { t } from '../i18n/en'

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
