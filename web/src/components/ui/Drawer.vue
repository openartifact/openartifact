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
  <DialogFrame
    placement="right"
    :open="open"
    :panel-class="`flex h-screen w-full flex-col border-l shadow-black/30 ${wide ? 'md:max-w-2xl' : 'md:max-w-md'}`"
    @close="requestClose"
  >
    <DialogSheet :title="title" :eyebrow="eyebrow" :description="description" @close="requestClose">
      <slot />
      <template v-if="$slots.footer" #footer><slot name="footer" /></template>
    </DialogSheet>
  </DialogFrame>
</template>

<script setup lang="ts">
import DialogFrame from './DialogFrame.vue'
import DialogSheet from './DialogSheet.vue'

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

function requestClose(): void {
  if (props.busy) return
  emit('update:open', false)
  emit('close')
}
</script>
