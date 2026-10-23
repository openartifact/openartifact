<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <StatusBadge :label="vexStatusLabel(status)" :variant="vexStatusVariant(status)" :data-vex-status="status" />
</template>

<script setup lang="ts">
import StatusBadge from './StatusBadge.vue'
import type { VexStatus } from '../api/vex'
import { vexStatusLabel, vexStatusVariant } from '../lib/vex'

defineProps<{ status: VexStatus }>()
</script>
