<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The FIRST EPSS score (the estimated probability of exploitation in the next 30
  days) as a percentage. The tooltip adds the percentile and the day of the score;
  `elevated` marks a score at or above the server's threshold (priority `epss`).
-->
<template>
  <span
    class="inline-flex items-center gap-1 whitespace-nowrap rounded px-1.5 py-0.5 text-xs font-medium tabular-nums"
    :class="elevated ? 'bg-primary-soft text-primary' : 'border border-border text-muted'"
    :title="title"
    data-testid="epss-badge"
  >
    {{ tp('security.exploit.epss.label', { score: formatEpss(score) }) }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatDate } from '../../lib/datetime'
import { formatEpss } from '../../lib/exploitPriority'
import { t, tp } from '../../i18n'

const props = defineProps<{
  /** Score, 0..1. */
  score: number
  /** Share of scored CVEs with the same or a lower score, 0..1. */
  percentile?: number | null
  /** Day of the score file (YYYY-MM-DD). */
  date?: string | null
  elevated?: boolean
}>()

const title = computed(() => {
  const lines = [t('security.exploit.epss.title')]
  if (props.percentile !== null && props.percentile !== undefined) {
    lines.push(tp('security.exploit.epss.percentile', { percentile: formatEpss(props.percentile) }))
  }
  if (props.date) lines.push(tp('security.exploit.epss.date', { date: formatDate(props.date) }))
  return lines.join('\n')
})
</script>
