<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <span
    class="inline-flex items-center gap-1.5 whitespace-nowrap text-xs font-medium"
    :class="color"
    :data-severity="severity"
  >
    <component :is="icon" class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
    {{ count === undefined ? label : tp('ui.severity.count', { count: String(count), label: label.toLowerCase() }) }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ExclamationTriangleIcon, InformationCircleIcon, MinusCircleIcon, XCircleIcon } from '@heroicons/vue/20/solid'
import { t, tp } from '../../i18n/en'

export type Severity = 'critical' | 'high' | 'medium' | 'low' | 'none'

const props = defineProps<{
  severity: Severity
  /** Shows "3 critical" instead of "Critical". */
  count?: number
}>()

const ICONS = {
  critical: XCircleIcon,
  high: ExclamationTriangleIcon,
  medium: ExclamationTriangleIcon,
  low: InformationCircleIcon,
  none: MinusCircleIcon,
}
const COLORS: Record<Severity, string> = {
  critical: 'text-danger',
  high: 'text-warning',
  medium: 'text-severity-medium',
  low: 'text-info',
  none: 'text-muted',
}

const icon = computed(() => ICONS[props.severity])
const color = computed(() => COLORS[props.severity])
const label = computed(() => t(`ui.severity.${props.severity}`))
</script>
