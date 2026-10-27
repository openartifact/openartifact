<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-4" data-testid="dry-run-result">
    <div class="flex items-start gap-2.5 rounded-lg border border-border bg-surface-hover/40 p-3" role="status">
      <InformationCircleIcon class="w-4 h-4 text-muted mt-0.5 shrink-0" aria-hidden="true" />
      <p class="text-xs text-foreground" data-testid="dry-run-no-changes">{{ t('tiering.dryRun.noChanges') }}</p>
    </div>

    <div
      v-if="result.truncated"
      class="flex items-start gap-2.5 rounded-lg border border-warning/30 bg-warning-soft p-3"
      role="alert"
      data-testid="dry-run-truncated"
    >
      <ExclamationTriangleIcon class="w-4 h-4 text-warning mt-0.5 shrink-0" aria-hidden="true" />
      <p class="text-xs text-warning">{{ t('tiering.dryRun.truncated') }}</p>
    </div>

    <dl class="grid grid-cols-3 gap-3">
      <div class="rounded-lg border border-border p-3">
        <dt class="text-caption text-muted">{{ t('tiering.dryRun.wouldMove') }}</dt>
        <dd class="text-xl font-bold tabular-nums text-foreground" data-testid="dry-run-count">
          {{ formatCount(result.would_move?.count) }}
        </dd>
      </div>
      <div class="rounded-lg border border-border p-3">
        <dt class="text-caption text-muted">{{ t('tiering.dryRun.totalSize') }}</dt>
        <dd class="text-xl font-bold tabular-nums text-foreground" data-testid="dry-run-bytes">
          {{ formatBytes(result.would_move?.bytes ?? 0) }}
        </dd>
      </div>
      <div class="rounded-lg border border-border p-3">
        <dt class="text-caption text-muted">{{ t('tiering.dryRun.scanned') }}</dt>
        <dd class="text-xl font-bold tabular-nums text-foreground">{{ formatCount(result.scanned) }}</dd>
      </div>
    </dl>

    <section v-if="routes.length" :aria-label="t('tiering.dryRun.routes')">
      <h3 class="text-xs font-semibold text-foreground mb-1.5">{{ t('tiering.dryRun.routes') }}</h3>
      <TableScroller>
      <table class="w-full text-sm">
        <thead>
          <tr class="text-left text-caption font-semibold text-muted">
            <th scope="col" class="py-1.5 pr-3">{{ t('tiering.dryRun.route') }}</th>
            <th scope="col" class="py-1.5 px-3 text-right">{{ t('tiering.dryRun.blobs') }}</th>
            <th scope="col" class="py-1.5 pl-3 text-right">{{ t('tiering.dryRun.size') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="r in routes" :key="`${r.from}>${r.to}`" class="group/row" data-testid="dry-run-route">
            <td class="py-1.5 pr-3 font-mono text-xs">{{ r.from }} → {{ r.to }}</td>
            <td class="py-1.5 px-3 text-right tabular-nums">{{ formatCount(r.count) }}</td>
            <td class="py-1.5 pl-3 text-right tabular-nums">{{ formatBytes(r.bytes ?? 0) }}</td>
          </tr>
        </tbody>
      </table>
      </TableScroller>
    </section>

    <section v-if="policies.length" :aria-label="t('tiering.dryRun.policies')">
      <h3 class="text-xs font-semibold text-foreground mb-1.5">{{ t('tiering.dryRun.policies') }}</h3>
      <TableScroller>
      <table class="w-full text-sm">
        <thead>
          <tr class="text-left text-caption font-semibold text-muted">
            <th scope="col" class="py-1.5 pr-3">{{ t('tiering.policy.column.name') }}</th>
            <th scope="col" class="py-1.5 px-3 text-right">{{ t('tiering.dryRun.blobs') }}</th>
            <th scope="col" class="py-1.5 pl-3 text-right">{{ t('tiering.dryRun.size') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          <tr v-for="p in policies" :key="p.policy" class="group/row" data-testid="dry-run-policy">
            <td class="py-1.5 pr-3">{{ p.policy || t('tiering.dryRun.promoteOnRead') }}</td>
            <td class="py-1.5 px-3 text-right tabular-nums">{{ formatCount(p.count) }}</td>
            <td class="py-1.5 pl-3 text-right tabular-nums">{{ formatBytes(p.bytes ?? 0) }}</td>
          </tr>
        </tbody>
      </table>
      </TableScroller>
    </section>

    <section v-if="sample.length" :aria-label="t('tiering.dryRun.sample')">
      <h3 class="text-xs font-semibold text-foreground mb-1.5">{{ t('tiering.dryRun.sample') }}</h3>
      <TableScroller>
      <table class="w-full text-sm">
          <thead>
            <tr class="text-left text-caption font-semibold text-muted">
              <th scope="col" class="py-1.5 pr-3">{{ t('tiering.dryRun.sha') }}</th>
              <th scope="col" class="py-1.5 px-3 text-right">{{ t('tiering.dryRun.size') }}</th>
              <th scope="col" class="py-1.5 px-3">{{ t('tiering.dryRun.route') }}</th>
              <th scope="col" class="py-1.5 px-3">{{ t('tiering.dryRun.policy') }}</th>
              <th scope="col" class="py-1.5 pl-3">{{ t('tiering.dryRun.lastAccessed') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            <tr v-for="s in sample" :key="s.sha256" class="group/row" data-testid="dry-run-sample">
              <td class="py-1.5 pr-3"><ShortSha :sha="s.sha256 ?? ''" /></td>
              <td class="py-1.5 px-3 text-right tabular-nums">{{ formatBytes(s.size_bytes ?? 0) }}</td>
              <td class="py-1.5 px-3 font-mono text-xs">{{ s.from }} → {{ s.to }}</td>
              <td class="py-1.5 px-3">{{ s.policy || t('tiering.dryRun.promoteOnRead') }}</td>
              <td class="py-1.5 pl-3 text-muted whitespace-nowrap">
                {{ formatDate(s.last_accessed_at, t('tiering.dryRun.neverAccessed')) }}
              </td>
            </tr>
          </tbody>
        </table>
      </TableScroller>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ExclamationTriangleIcon, InformationCircleIcon } from '@heroicons/vue/24/outline'
import ShortSha from './ShortSha.vue'
import TableScroller from './ui/TableScroller.vue'
import type { TierDryRun } from '../api/tiering'
import { t } from '../i18n'
import { formatDate } from '../lib/datetime'
import { formatBytes } from '../lib/quota'
import { formatCount } from '../lib/tiering'

const props = defineProps<{ result: TierDryRun }>()

const routes = computed(() => props.result.routes ?? [])
const policies = computed(() => props.result.policies ?? [])
const sample = computed(() => props.result.sample ?? [])
</script>
