<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The shared shell of every modal surface (confirm dialog, admin dialog,
  drawer, command palette): Headless UI Dialog with a fading backdrop and a
  panel that scales in (`center`, `top`) or slides in from the right (`right`).
  Focus is trapped, Escape and the backdrop emit `close`, and focus returns to
  the element that opened it. The default slot is the panel's content; callers
  add their own size and padding through `panelClass`.
-->
<template>
  <TransitionRoot appear :show="open" as="template" @after-leave="onAfterLeave">
    <Dialog as="div" class="relative z-50" :aria-label="label" @close="emit('close')">
      <TransitionChild
        as="template"
        enter="motion-safe:duration-150 motion-safe:ease-out"
        enter-from="opacity-0"
        enter-to="opacity-100"
        leave="motion-safe:duration-100 motion-safe:ease-in"
        leave-from="opacity-100"
        leave-to="opacity-0"
      >
        <div class="fixed inset-0" :class="backdropClass" aria-hidden="true" />
      </TransitionChild>

      <div class="fixed inset-0 overflow-y-auto">
        <div class="flex min-h-full" :class="layout.align">
          <TransitionChild
            as="template"
            :enter="layout.enter"
            :enter-from="layout.hidden"
            :enter-to="layout.shown"
            :leave="layout.leave"
            :leave-from="layout.shown"
            :leave-to="layout.hidden"
          >
            <DialogPanel ref="panel" v-bind="$attrs" class="border-border bg-surface shadow-2xl" :class="[layout.panel, panelClass]">
              <slot />
            </DialogPanel>
          </TransitionChild>
        </div>
      </div>
    </Dialog>
  </TransitionRoot>
</template>

<script setup lang="ts">
import { computed, ref, toRef, watch } from 'vue'
import { Dialog, DialogPanel, TransitionChild, TransitionRoot } from '@headlessui/vue'
import { useRestoreFocus } from '../../composables/useRestoreFocus'

defineOptions({ inheritAttrs: false })

type Placement = 'center' | 'right' | 'top'

const LAYOUTS: Record<
  Placement,
  { align: string; panel: string; enter: string; leave: string; hidden: string; shown: string }
> = {
  center: {
    align: 'items-center justify-center p-4',
    panel: 'transform transition-all',
    enter: 'motion-safe:duration-200 motion-safe:ease-out',
    leave: 'motion-safe:duration-150 motion-safe:ease-in',
    hidden: 'opacity-0 scale-95',
    shown: 'opacity-100 scale-100',
  },
  right: {
    align: 'items-stretch justify-end',
    panel: 'transition-transform',
    enter: 'motion-safe:duration-200 motion-safe:ease-out',
    leave: 'motion-safe:duration-150 motion-safe:ease-in',
    hidden: 'translate-x-full',
    shown: 'translate-x-0',
  },
  top: {
    align: 'items-start justify-center p-4 pt-[10vh]',
    panel: 'transform',
    enter: 'motion-safe:duration-150 motion-safe:ease-out',
    leave: 'motion-safe:duration-100 motion-safe:ease-in',
    hidden: 'opacity-0 scale-95',
    shown: 'opacity-100 scale-100',
  },
}

const props = withDefaults(
  defineProps<{
    open: boolean
    placement?: Placement
    /** Size, border, padding and layout of the panel. */
    panelClass?: string
    backdropClass?: string
    /** Accessible name when the content has no `DialogTitle`. */
    label?: string
  }>(),
  { placement: 'center', panelClass: undefined, backdropClass: 'bg-black/50', label: undefined },
)

const emit = defineEmits<{ close: []; 'after-leave': [] }>()

const layout = computed(() => LAYOUTS[props.placement])

const { restoreFocus } = useRestoreFocus(toRef(props, 'open'))

function onAfterLeave(): void {
  emit('after-leave')
  restoreFocus()
}

// A form drawer marks the field to start in (`data-autofocus`); otherwise the dialog keeps its first control.
const panel = ref<{ $el: HTMLElement } | null>(null)
watch(
  () => props.open,
  (open) => {
    // After the dialog's own focus trap has picked its first control.
    if (open) setTimeout(() => panel.value?.$el.querySelector<HTMLElement>('[data-autofocus]')?.focus(), 0)
  },
  { immediate: true },
)
</script>
