<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="flex flex-col items-center justify-center py-16 px-4 text-center">
    <h3 class="text-base font-semibold text-foreground mb-1">{{ title }}</h3>
    <p v-if="description" class="text-sm text-muted max-w-sm">{{ description }}</p>
    <router-link
      v-if="actionLabel && actionTo"
      :to="actionTo"
      class="mt-4 inline-flex items-center gap-2 px-4 py-2 rounded-lg bg-primary text-primary-contrast text-sm font-medium hover:bg-primary/90 transition-colors active:scale-[0.97]"
    >
      {{ actionLabel }}
    </router-link>
  </div>
</template>

<script setup lang="ts">
defineProps<{
  title: string
  description?: string
  actionLabel?: string
  actionTo?: string
}>()
</script>
