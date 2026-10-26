<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Security of one repository row: the worst finding with its word (never colour alone) and how many more there are; the title spells out every count. -->
<template>
  <span v-if="state.kind === 'unknown' || state.kind === 'empty'" class="text-xs text-muted">—</span>
  <span v-else-if="state.kind === 'unscanned'" class="text-xs text-muted">{{ t('rl.security.unscanned') }}</span>
  <span v-else-if="state.kind === 'clean'" class="inline-flex items-center gap-1.5 text-xs font-medium text-success">
    <CheckCircleIcon class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
    {{ t('rl.security.clean') }}
  </span>
  <span v-else class="inline-flex flex-wrap items-center gap-x-2" :title="breakdown" data-testid="security-findings">
    <SeverityBadge v-if="worst.kind === 'severity'" :severity="worst.severity" :count="worst.count" />
    <span v-else :class="['text-xs font-medium', worst.kind === 'secrets' ? 'text-danger' : 'text-warning']">{{ worst.label }}</span>
    <span v-if="more > 0" class="text-xs text-muted" data-testid="security-more">{{ tp('rl.security.more', { n: String(more) }) }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircleIcon } from '@heroicons/vue/20/solid'
import SeverityBadge from '../ui/SeverityBadge.vue'
import { securityState } from '../../lib/repoList'
import type { RepositoryListItem } from '../../api/repositories'
import { t, tp, tpn } from '../../i18n/en'

const props = defineProps<{ item: Pick<RepositoryListItem, 'stats'> }>()
const state = computed(() => securityState(props.item))

const SEVERITIES = ['critical', 'high', 'medium', 'low'] as const

type Finding =
  | { kind: 'severity'; severity: (typeof SEVERITIES)[number]; count: number; label: string }
  | { kind: 'secrets' | 'licenses'; count: number; label: string }

// Worst first: vulnerabilities by severity, then secrets, then license issues.
const findings = computed<Finding[]>(() => {
  if (state.value.kind !== 'findings') return []
  const s = state.value.security
  const list: Finding[] = SEVERITIES.filter((k) => s[k] > 0).map((k) => ({ kind: 'severity', severity: k, count: s[k], label: `${s[k]} ${k}` }))
  if (s.secrets) list.push({ kind: 'secrets', count: s.secrets, label: tpn('rl.security.secrets', s.secrets) })
  if (s.license_violations) list.push({ kind: 'licenses', count: s.license_violations, label: tpn('rl.security.licenses', s.license_violations) })
  return list
})
const worst = computed(() => findings.value[0] ?? { kind: 'licenses' as const, count: 0, label: '' })
const more = computed(() => findings.value.slice(1).reduce((n, f) => n + f.count, 0))
const breakdown = computed(() => findings.value.map((f) => f.label).join(', '))
</script>
