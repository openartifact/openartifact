<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <span
    v-if="role"
    class="inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium"
    :class="role === 'owner' ? 'bg-primary-soft text-primary' : 'bg-surface-hover text-foreground/80'"
    data-testid="role-badge"
  >{{ tDynamic(`projects.role.${role}`) }}</span>
</template>

<script setup lang="ts">
import { tDynamic } from '../i18n'

defineProps<{ role?: string }>()
</script>
