<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Right-side panel on Headless UI's Dialog: focus is trapped, Escape and the
  backdrop close it, and focus returns to the element that opened it. Full
  width below 768px.
-->
<template>
  <TransitionRoot :show="open" as="template" appear @after-leave="restoreFocus">
    <Dialog as="div" class="relative z-50" @close="requestClose">
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

      <div class="fixed inset-0 flex justify-end overflow-hidden">
        <TransitionChild
          as="template"
          enter="motion-safe:duration-200 motion-safe:ease-out"
          enter-from="translate-x-full"
          enter-to="translate-x-0"
          leave="motion-safe:duration-150 motion-safe:ease-in"
          leave-from="translate-x-0"
          leave-to="translate-x-full"
        >
          <DialogPanel
            class="flex h-full w-full flex-col border-l border-border bg-surface shadow-2xl shadow-black/30 transition-transform"
            :class="wide ? 'md:max-w-2xl' : 'md:max-w-md'"
          >
            <div class="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
              <div class="min-w-0">
                <p v-if="eyebrow" class="font-mono text-caption text-muted">{{ eyebrow }}</p>
                <DialogTitle as="h2" class="text-lg font-semibold tracking-tight text-foreground">{{ title }}</DialogTitle>
                <DialogDescription v-if="description" class="mt-1 text-sm text-muted">{{ description }}</DialogDescription>
              </div>
              <button
                type="button"
                :aria-label="t('common.close')"
                class="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded text-muted hover:bg-surface-hover hover:text-foreground max-md:h-11 max-md:w-11"
                @click="requestClose"
              >
                <XMarkIcon class="h-5 w-5" aria-hidden="true" />
              </button>
            </div>
            <div class="nice-scroll flex-1 overflow-y-auto px-5 py-5">
              <slot />
            </div>
            <div
              v-if="$slots.footer"
              class="flex flex-wrap items-center justify-end gap-2 border-t border-border bg-surface-hover/30 px-5 py-3"
            >
              <slot name="footer" />
            </div>
          </DialogPanel>
        </TransitionChild>
      </div>
    </Dialog>
  </TransitionRoot>
</template>

<script setup lang="ts">
import {
  Dialog,
  DialogDescription,
  DialogPanel,
  DialogTitle,
  TransitionChild,
  TransitionRoot,
} from '@headlessui/vue'
import { XMarkIcon } from '@heroicons/vue/24/outline'
import { toRef } from 'vue'
import { useRestoreFocus } from '../../composables/useRestoreFocus'
import { t } from '../../i18n/en'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    eyebrow?: string
    description?: string
    /** Wider panel on desktop (tables, previews). */
    wide?: boolean
    /** Blocks closing (Escape, backdrop, X) while a request is running. */
    busy?: boolean
  }>(),
  { eyebrow: undefined, description: undefined, wide: false, busy: false },
)

const emit = defineEmits<{ close: []; 'update:open': [open: boolean] }>()

const { restoreFocus } = useRestoreFocus(toRef(props, 'open'))

function requestClose(): void {
  if (props.busy) return
  emit('update:open', false)
  emit('close')
}
</script>
