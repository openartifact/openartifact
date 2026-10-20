<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <TransitionRoot appear :show="open" as="template">
    <Dialog as="div" class="relative z-50" @close="emit('close')">
      <TransitionChild
        as="template"
        enter="motion-safe:duration-150 motion-safe:ease-out"
        enter-from="opacity-0"
        enter-to="opacity-100"
        leave="motion-safe:duration-100 motion-safe:ease-in"
        leave-from="opacity-100"
        leave-to="opacity-0"
      >
        <div class="fixed inset-0 bg-black/40 backdrop-blur-sm" aria-hidden="true" />
      </TransitionChild>

      <div class="fixed inset-0 overflow-y-auto">
        <div
          class="flex min-h-full"
          :class="drawer ? 'items-stretch justify-end' : 'items-center justify-center p-4'"
        >
          <TransitionChild
            as="template"
            enter="motion-safe:duration-200 motion-safe:ease-out"
            :enter-from="drawer ? 'translate-x-full' : 'opacity-0 scale-95'"
            :enter-to="drawer ? 'translate-x-0' : 'opacity-100 scale-100'"
            leave="motion-safe:duration-150 motion-safe:ease-in"
            :leave-from="drawer ? 'translate-x-0' : 'opacity-100 scale-100'"
            :leave-to="drawer ? 'translate-x-full' : 'opacity-0 scale-95'"
          >
            <DialogPanel
              class="relative w-full bg-surface border-border shadow-2xl shadow-black/30 flex flex-col transform transition-all"
              :class="
                drawer
                  ? `${widthClass} border-l min-h-screen`
                  : `${widthClass} rounded-xl border max-h-[90vh]`
              "
            >
              <div class="px-6 py-4 border-b border-border flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <div v-if="eyebrow" class="text-[11px] uppercase tracking-[0.14em] text-muted font-mono">
                    {{ eyebrow }}
                  </div>
                  <DialogTitle as="h2" class="text-lg font-semibold tracking-tight text-foreground">
                    {{ title }}
                  </DialogTitle>
                  <DialogDescription v-if="description" class="mt-1 text-sm text-muted">
                    {{ description }}
                  </DialogDescription>
                </div>
                <button
                  type="button"
                  :aria-label="t('common.close')"
                  class="text-muted hover:text-foreground p-1 rounded hover:bg-surface-hover"
                  @click="emit('close')"
                >
                  <XMarkIcon class="w-5 h-5" aria-hidden="true" />
                </button>
              </div>
              <div class="flex-1 overflow-y-auto nice-scroll px-6 py-5">
                <slot />
              </div>
              <div
                v-if="$slots.footer"
                class="px-6 py-3 border-t border-border bg-surface-hover/30 flex items-center justify-end gap-2"
              >
                <slot name="footer" />
              </div>
            </DialogPanel>
          </TransitionChild>
        </div>
      </div>
    </Dialog>
  </TransitionRoot>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import {
  Dialog,
  DialogPanel,
  DialogTitle,
  DialogDescription,
  TransitionRoot,
  TransitionChild,
} from '@headlessui/vue'
import { XMarkIcon } from '@heroicons/vue/24/outline'
import { t } from '../i18n/en'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    eyebrow?: string
    description?: string
    /** `drawer` slides in from the right; otherwise a centred modal. */
    drawer?: boolean
    /** Tailwind max-width class for the panel. */
    maxWidth?: string
  }>(),
  { drawer: false, maxWidth: undefined, eyebrow: undefined, description: undefined },
)

const emit = defineEmits<{ (e: 'close'): void }>()

const widthClass = computed(() => props.maxWidth ?? (props.drawer ? 'max-w-xl' : 'max-w-lg'))
</script>
