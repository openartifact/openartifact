<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Text with one highlighted range (a search hit). The mark keeps the text readable without colour. -->
<template>
  <template v-if="range">{{ text.slice(0, range[0]) }}<mark class="rounded-sm bg-primary-soft px-0.5 text-primary">{{ text.slice(range[0], range[1]) }}</mark>{{ text.slice(range[1]) }}</template>
  <template v-else>{{ text }}</template>
</template>

<script setup lang="ts">
defineProps<{ text: string; range: [number, number] | null }>()
</script>
