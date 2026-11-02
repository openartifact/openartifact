<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Vulnerability scans with their exploitation signals: the ones that meet the gate (check, block) or the ones already blocked (release). -->
<template>
  <ListTable
    :columns="columns"
    :caption="caption"
    :rows="rows"
    row-key="id"
    :loading="loading"
    :error="error"
    :empty-title="emptyTitle"
    :empty-hint="emptyHint"
    @retry="emit('retry')"
  >
    <template #cell-package="{ row }">
      <div class="min-w-0">
        <p class="truncate font-medium text-foreground" :title="pkg(row as SecurityFinding)">{{ pkg(row as SecurityFinding) }}</p>
        <p class="truncate text-xs text-muted">{{ (row as SecurityFinding).repository }}</p>
      </div>
    </template>
    <template #cell-priority="{ row }">
      <PriorityBadge :priority="(row as SecurityFinding).priority" />
    </template>
    <template #cell-signals="{ row }">
      <span class="text-xs text-muted">{{ signals(row as SecurityFinding) }}</span>
    </template>
    <template #cell-verdict="{ row }">
      <div v-if="verdicts[(row as SecurityFinding).id]" class="text-xs" data-testid="verdict">
        <span v-if="verdicts[(row as SecurityFinding).id]!.loading" class="text-muted">{{ t('common.loading') }}</span>
        <span v-else-if="verdicts[(row as SecurityFinding).id]!.error" class="text-danger" role="alert">{{ verdicts[(row as SecurityFinding).id]!.error }}</span>
        <template v-else-if="verdicts[(row as SecurityFinding).id]!.result">
          <StatusBadge
            :label="verdicts[(row as SecurityFinding).id]!.result!.pass ? t('sx.gate.pass') : t('sx.gate.fail')"
            :variant="verdicts[(row as SecurityFinding).id]!.result!.pass ? 'success' : 'danger'"
          />
          <ul v-if="!verdicts[(row as SecurityFinding).id]!.result!.pass" class="mt-1 space-y-0.5">
            <li v-for="f in verdicts[(row as SecurityFinding).id]!.result!.failed.slice(0, 5)" :key="f.id" class="break-words font-mono text-muted">
              {{ f.cve || f.id }}: {{ f.reasons.map(reasonText).join(', ') }}
            </li>
            <li v-if="verdicts[(row as SecurityFinding).id]!.result!.failed.length > 5" class="text-muted">
              {{ tp('sx.gate.more', { count: String(verdicts[(row as SecurityFinding).id]!.result!.failed.length - 5) }) }}
            </li>
          </ul>
        </template>
      </div>
    </template>
    <template #cell-actions="{ row }">
      <RowActions :actions="actionsOf(row as SecurityFinding)" :subject="pkg(row as SecurityFinding)" @action="(key) => onAction(key, row as SecurityFinding)" />
    </template>
  </ListTable>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { LockClosedIcon, LockOpenIcon, ShieldCheckIcon } from '@heroicons/vue/20/solid'
import StatusBadge from './StatusBadge.vue'
import PriorityBadge from './security/PriorityBadge.vue'
import ListTable, { type ListColumn } from './ui/ListTable.vue'
import RowActions, { type RowAction } from './ui/RowActions.vue'
import type { SecurityFinding } from '../api/security'
import type { Verdict } from '../composables/useScanGate'
import { formatEpss } from '../lib/exploitPriority'
import { reasonParts } from '../lib/scanGate'
import { t, tp } from '../i18n'

const props = defineProps<{
  mode: 'candidates' | 'blocked'
  rows: SecurityFinding[]
  caption: string
  emptyTitle: string
  emptyHint: string
  loading?: boolean
  error?: string
  verdicts?: Record<string, Verdict>
  changing: ReadonlySet<string>
}>()
const emit = defineEmits<{ check: [f: SecurityFinding]; block: [f: SecurityFinding]; release: [f: SecurityFinding]; retry: [] }>()

const verdicts = computed(() => props.verdicts ?? {})

const columns = computed<ListColumn[]>(() => [
  { key: 'package', label: t('sx.gate.col.package'), grow: true },
  { key: 'priority', label: t('sx.gate.col.priority') },
  { key: 'signals', label: t('sx.gate.col.signals'), dropOrder: 1 },
  ...(props.mode === 'candidates' ? [{ key: 'verdict', label: t('sx.gate.col.verdict') }] : []),
  { key: 'actions', label: t('common.actions'), actions: true },
])

const pkg = (f: SecurityFinding): string => `${f.package_name}@${f.package_version}`

function signals(f: SecurityFinding): string {
  const parts: string[] = []
  if (f.kev_count > 0) parts.push(tp('sx.gate.signal.kev', { count: String(f.kev_count) }))
  if (f.max_epss !== null) parts.push(tp('sx.gate.signal.epss', { score: formatEpss(f.max_epss) }))
  return parts.join(' · ') || '—'
}

function reasonText(reason: string): string {
  const p = reasonParts(reason)
  if (p.kind === 'kev') return t('sx.gate.reason.kev')
  if (p.kind === 'epss') return tp('sx.gate.reason.epss', { min: formatEpss(Number(p.value)) })
  return reason
}

function actionsOf(f: SecurityFinding): RowAction[] {
  const busy = props.changing.has(f.id)
  return props.mode === 'candidates'
    ? [
        { key: 'check', label: t('sx.gate.check'), icon: ShieldCheckIcon, loading: verdicts.value[f.id]?.loading },
        { key: 'block', label: t('sx.gate.block'), icon: LockClosedIcon, tone: 'danger', disabled: busy },
      ]
    : [{ key: 'release', label: t('sx.gate.release'), icon: LockOpenIcon, disabled: busy }]
}

function onAction(key: string, f: SecurityFinding): void {
  if (key === 'check') emit('check', f)
  else if (key === 'block') emit('block', f)
  else if (key === 'release') emit('release', f)
}
</script>
