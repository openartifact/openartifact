<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- The answer of a diff or an apply: manifest problems, plan errors, and the change list (field, old value, new value). -->
<template>
  <section class="rounded-lg border border-border bg-surface" :aria-label="t('sx.declarative.plan')" data-testid="declarative-plan">
    <header class="flex flex-wrap items-center gap-2 border-b border-border px-5 py-3">
      <h2 class="mr-auto text-sm font-semibold text-foreground">{{ applied ? t('sx.declarative.result') : t('sx.declarative.plan') }}</h2>
      <StatusBadge v-for="c in counts" :key="c.action" :label="tp('sx.declarative.count', { count: c.count, action: t(`sx.declarative.action.${c.action}`) })" :variant="ACTION_VARIANT[c.action]" />
    </header>

    <div v-if="plan.code === 'manifest_invalid'" class="space-y-2 border-b border-border px-5 py-4" role="alert" data-testid="plan-problems">
      <p class="flex items-center gap-2 text-sm font-medium text-danger">
        <ExclamationTriangleIcon class="h-4 w-4 shrink-0" aria-hidden="true" />
        {{ t('sx.declarative.invalid') }}
      </p>
      <ul class="space-y-1 text-sm">
        <li v-for="(p, i) in plan.problems ?? []" :key="i" class="flex flex-wrap gap-x-2">
          <code v-if="p.line" class="font-mono text-xs text-muted">{{ tp('sx.declarative.line', { line: String(p.line) }) }}</code>
          <span v-if="p.object" class="font-mono text-xs text-foreground">{{ p.object }}</span>
          <span class="text-foreground">{{ p.field ? `${p.field}: ${p.message}` : p.message }}</span>
        </li>
      </ul>
    </div>

    <div v-else-if="error && !(plan.errors?.length)" class="border-b border-border px-5 py-4 text-sm text-danger" role="alert" data-testid="plan-error">
      {{ error }}
    </div>

    <div v-if="plan.errors?.length" class="space-y-1 border-b border-border px-5 py-4 text-sm" role="alert" data-testid="plan-errors">
      <p class="font-medium text-danger">{{ t('sx.declarative.unapplicable') }}</p>
      <ul class="list-disc space-y-0.5 pl-5 text-foreground">
        <li v-for="(e, i) in plan.errors" :key="i" class="break-words">{{ e }}</li>
      </ul>
    </div>

    <p v-if="applied" class="border-b border-border px-5 py-3 text-sm text-muted" role="status" data-testid="plan-outcome">
      {{ tp('sx.declarative.outcome', { applied: outcome.applied, failed: outcome.failed, skipped: outcome.skipped }) }}
    </p>

    <p v-if="!plan.code && !plan.changes && !applied" class="px-5 py-4 text-sm text-muted" data-testid="plan-nochanges">
      {{ t('sx.declarative.noChanges') }}
    </p>

    <ul v-if="!plan.code || plan.items.length" class="divide-y divide-border" data-testid="plan-items">
      <li v-for="item in items" :key="`${item.kind}/${item.name}`" class="px-5 py-3" :data-action="item.action">
        <div class="flex flex-wrap items-center gap-2">
          <StatusBadge :label="t(`sx.declarative.action.${item.action}`)" :variant="ACTION_VARIANT[item.action]" />
          <span class="text-xs text-muted">{{ item.kind }}</span>
          <span class="min-w-0 break-all font-mono text-sm font-medium text-foreground">{{ item.name }}</span>
          <span v-if="item.action === 'delete'" class="text-xs text-muted">{{ item.managed ? t('sx.declarative.deleteManaged') : t('sx.declarative.deleteUnmanaged') }}</span>
          <span v-if="item.adopt" class="text-xs text-muted">{{ t('sx.declarative.adopt') }}</span>
          <StatusBadge v-if="item.result" class="ml-auto" :label="t(`sx.declarative.result.${item.result}`)" :variant="RESULT_VARIANT[item.result]" />
        </div>
        <p v-if="item.error" class="mt-1 break-words text-sm text-danger" role="alert">{{ item.error }}</p>
        <ul v-if="item.action !== 'delete' && item.changes?.length" class="mt-2 space-y-0.5 font-mono text-xs">
          <li v-for="c in item.changes" :key="c.path" class="flex flex-wrap items-baseline gap-x-2">
            <span class="text-muted">{{ c.path }}</span>
            <template v-if="changeKind(c, item.action) === 'secret'">
              <span class="text-muted">{{ t('sx.declarative.secret') }}</span>
            </template>
            <template v-else>
              <span v-if="changeKind(c, item.action) !== 'added'" class="rounded bg-danger-soft px-1 text-danger line-through">{{ formatValue(c.old) }}</span>
              <template v-if="changeKind(c, item.action) !== 'removed'">
                <span v-if="changeKind(c, item.action) === 'replaced'" class="text-muted" aria-hidden="true">→</span>
                <span class="rounded bg-success-soft px-1 text-success">{{ formatValue(c.new) }}</span>
              </template>
            </template>
          </li>
        </ul>
      </li>
    </ul>

    <div v-if="quiet > 0" class="border-t border-border px-5 py-2">
      <button type="button" class="text-sm text-primary hover:underline" data-testid="toggle-unchanged" @click="showQuiet = !showQuiet">
        {{ showQuiet ? t('sx.declarative.hideUnchanged') : tp('sx.declarative.showUnchanged', { count: quiet }) }}
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ExclamationTriangleIcon } from '@heroicons/vue/20/solid'
import StatusBadge from './StatusBadge.vue'
import type { DeclarativeRun } from '../api/declarative'
import { changeKind, formatValue, isQuiet, listedItems, outcomeCounts, summaryCounts, type PlanAction } from '../lib/declarativePlan'
import { t, tp } from '../i18n'

const props = defineProps<{ plan: DeclarativeRun; applied: boolean; error: string }>()

type Variant = 'success' | 'warning' | 'danger' | 'neutral'
const ACTION_VARIANT: Record<PlanAction, Variant> = { create: 'success', update: 'warning', delete: 'danger', unchanged: 'neutral' }
const RESULT_VARIANT: Record<'applied' | 'failed' | 'skipped', Variant> = { applied: 'success', failed: 'danger', skipped: 'neutral' }

const showQuiet = ref(false)
const counts = computed(() => summaryCounts(props.plan))
const items = computed(() => listedItems(props.plan, showQuiet.value))
const quiet = computed(() => props.plan.items.filter(isQuiet).length)
const outcome = computed(() => outcomeCounts(props.plan))
</script>
