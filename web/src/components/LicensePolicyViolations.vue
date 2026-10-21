<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <ul v-if="violations.length" class="space-y-1" :aria-label="t('sec.license.violations')">
    <li
      v-for="(v, i) in violations"
      :key="`${v.policy_id ?? ''}-${i}`"
      class="flex items-center gap-2 text-xs rounded-md px-2 py-1"
      :class="v.action === 'block' ? 'bg-danger-soft/60 border border-danger/30 font-semibold' : 'bg-surface-hover/40'"
      :data-action="v.action"
    >
      <StatusBadge :label="licenseActionLabel(v.action)" :variant="licenseActionVariant(v.action)" />
      <span class="font-mono" :class="v.action === 'block' ? 'text-danger' : 'text-foreground/80'">{{ v.violation }}</span>
      <span v-if="v.policy_name" class="text-muted">· {{ v.policy_name }}</span>
    </li>
  </ul>
</template>

<script setup lang="ts">
import StatusBadge from './StatusBadge.vue'
import { licenseActionLabel, licenseActionVariant } from '../lib/scanStatus'
import type { LicensePolicyViolation } from '../api/security'
import { t } from '../i18n/en'

defineProps<{
  violations: readonly LicensePolicyViolation[]
}>()
</script>
