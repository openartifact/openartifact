<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="flex flex-col items-center justify-center py-16 px-4 text-center">
    <h3 class="text-base font-semibold text-foreground mb-1">{{ title }}</h3>
    <p v-if="description" class="text-sm text-muted max-w-sm">{{ description }}</p>
    <AppButton v-if="actionLabel && actionTo" class="mt-4" :to="actionTo">{{ actionLabel }}</AppButton>
  </div>
</template>

<script setup lang="ts">
import AppButton from './AppButton.vue'
defineProps<{
  title: string
  description?: string
  actionLabel?: string
  actionTo?: string
}>()
</script>
