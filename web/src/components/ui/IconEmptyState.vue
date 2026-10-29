<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Empty list inside a card: an icon, what is missing, a hint, and the button that adds the first entry. -->
<template>
  <div class="py-16 text-center">
    <div class="w-12 h-12 rounded-full bg-surface-hover mx-auto flex items-center justify-center mb-3">
      <component :is="icon" class="w-6 h-6 text-muted" aria-hidden="true" />
    </div>
    <p class="text-sm font-medium text-foreground">{{ title }}</p>
    <p class="text-xs text-muted mt-1 mb-4">{{ hint }}</p>
    <AppButton size="sm" @click="emit('action')">
      <PlusIcon class="w-4 h-4" aria-hidden="true" />
      {{ actionLabel }}
    </AppButton>
  </div>
</template>

<script setup lang="ts">
import type { Component } from 'vue'
import { PlusIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'

defineProps<{ icon: Component; title: string; hint: string; actionLabel: string }>()

const emit = defineEmits<{ action: [] }>()
</script>
