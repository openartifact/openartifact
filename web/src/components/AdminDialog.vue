<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <DialogFrame :open="open" :placement="drawer ? 'right' : 'center'" :panel-class="panelClass" @close="emit('close')">
    <DialogSheet :title="title" :eyebrow="eyebrow" :description="description" @close="emit('close')">
      <slot />
      <template v-if="$slots.footer" #footer><slot name="footer" /></template>
    </DialogSheet>
  </DialogFrame>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import DialogFrame from './ui/DialogFrame.vue'
import DialogSheet from './ui/DialogSheet.vue'

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

const panelClass = computed(() => {
  const width = props.maxWidth ?? (props.drawer ? 'max-w-xl' : 'max-w-lg')
  const shape = props.drawer ? 'h-screen border-l' : 'max-h-[90vh] rounded-xl border'
  return `relative flex w-full flex-col shadow-black/30 ${width} ${shape}`
})
</script>
