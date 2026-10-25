<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Header actions of a settings section. The section page owns the PageHeader (breadcrumbs, title,
  description); a section puts its buttons here and they render in the header's actions area.
  Mounted outside a section page (tests, embedding) the buttons render in place.
-->
<template>
  <Teleport v-if="target" :to="target">
    <slot />
  </Teleport>
  <div v-else-if="!hosted" class="mb-4 flex flex-wrap items-center justify-end gap-2">
    <slot />
  </div>
</template>

<script setup lang="ts">
import { computed, inject } from 'vue'
import { SETTINGS_ACTIONS_TARGET } from '../lib/settingsActions'

const host = inject(SETTINGS_ACTIONS_TARGET, null)
const hosted = host !== null
const target = computed(() => host?.value ?? null)
</script>
