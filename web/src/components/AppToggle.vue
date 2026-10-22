<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <button
    type="button"
    role="switch"
    :aria-checked="modelValue"
    :aria-label="label"
    :aria-labelledby="labelledby"
    :disabled="disabled"
    class="relative inline-flex shrink-0 items-center rounded-full transition-colors duration-200 disabled:opacity-50 disabled:cursor-not-allowed"
    :class="[size === 'md' ? 'h-6 w-11' : 'h-5 w-9', modelValue ? 'bg-primary' : 'bg-border-strong']"
    @click="emit('update:modelValue', !modelValue)"
  >
    <span
      class="inline-block rounded-full bg-white shadow-sm transition-transform duration-200"
      :class="[
        size === 'md' ? 'h-5 w-5' : 'h-4 w-4',
        modelValue ? (size === 'md' ? 'translate-x-[1.375rem]' : 'translate-x-[1.125rem]') : 'translate-x-0.5',
      ]"
      aria-hidden="true"
    />
  </button>
</template>

<script setup lang="ts">
withDefaults(
  defineProps<{
    modelValue: boolean
    /** Accessible name when no visible label element exists. */
    label?: string
    /** id of the visible label element, preferred over `label`. */
    labelledby?: string
    size?: 'sm' | 'md'
    disabled?: boolean
  }>(),
  { label: undefined, labelledby: undefined, size: 'sm', disabled: false },
)

const emit = defineEmits<{ (e: 'update:modelValue', value: boolean): void }>()
</script>
