<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Everyone with read access sees what the scans counted. Maintainers and admins also
  see the findings and can record VEX decisions: the findings name vulnerabilities,
  which the server withholds from plain readers.
-->
<template>
  <div class="space-y-8">
    <section aria-labelledby="artifact-sec-summary">
      <h2 id="artifact-sec-summary" class="mb-3 text-base font-semibold text-foreground">{{ t('artifact.security.summary') }}</h2>
      <template v-if="summary">
        <p v-if="level === 'unscanned'" class="max-w-2xl text-sm text-muted" data-testid="sec-unscanned">
          <span class="font-medium text-foreground">{{ t('artifact.security.unscanned') }}</span>
          {{ t('artifact.security.unscannedHint') }}
        </p>
        <template v-else>
          <p class="mb-3 text-sm text-muted" data-testid="sec-scanned">
            <template v-if="level === 'clean'">{{ t('artifact.security.clean') }} </template>
            {{ tp('artifact.security.lastScanned', { when: summary.scanned_at ? formatDateTime(summary.scanned_at) : '–' }) }}
          </p>
          <dl class="grid max-w-3xl grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-4">
            <div v-for="s in SEVERITIES" :key="s">
              <dt class="text-xs text-muted">{{ t(`ui.severity.${s}`) }}</dt>
              <dd class="mt-0.5"><SeverityBadge :severity="s" :count="summary.vuln[s]" /></dd>
            </div>
          </dl>
          <dl class="mt-4 flex flex-wrap gap-x-8 gap-y-3 text-sm">
            <div>
              <dt class="text-xs text-muted">{{ t('artifact.security.license') }}</dt>
              <dd class="mt-0.5 tabular-nums" data-testid="sec-license">
                <router-link v-if="summary.license.violations > 0 && ctx.caps.value.maintain" :to="scanLink('license')" class="text-primary hover:underline">{{ tp('artifact.security.violations', { count: String(summary.license.violations) }) }}</router-link>
                <template v-else>{{ tp('artifact.security.violations', { count: String(summary.license.violations) }) }}</template>
              </dd>
            </div>
            <div>
              <dt class="text-xs text-muted">{{ t('artifact.security.secrets') }}</dt>
              <dd class="mt-0.5 tabular-nums" data-testid="sec-secret">
                <router-link v-if="summary.secret.count > 0 && ctx.caps.value.maintain" :to="scanLink('secret')" class="text-primary hover:underline">{{ tp('artifact.security.secretCount', { count: String(summary.secret.count) }) }}</router-link>
                <template v-else>{{ tp('artifact.security.secretCount', { count: String(summary.secret.count) }) }}</template>
              </dd>
            </div>
            <div v-if="summary.ai.quarantine_recommended + summary.ai.review_required > 0">
              <dt class="text-xs text-muted">{{ t('artifact.security.ai') }}</dt>
              <dd class="mt-0.5 tabular-nums">{{ tp('artifact.security.aiCount', { quarantine: String(summary.ai.quarantine_recommended), review: String(summary.ai.review_required) }) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-muted">{{ t('artifact.security.quarantine') }}</dt>
              <dd class="mt-0.5" data-testid="sec-quarantine">{{ summary.quarantined ? t('artifact.security.quarantined') : t('artifact.security.notQuarantined') }}</dd>
            </div>
          </dl>
        </template>
      </template>
      <p v-else class="text-sm text-muted">{{ t('common.loading') }}</p>
    </section>

    <section aria-labelledby="artifact-sec-findings">
      <h2 id="artifact-sec-findings" class="mb-3 text-base font-semibold text-foreground">{{ t('artifact.security.findings') }}</h2>
      <p v-if="!ctx.caps.value.maintain" class="max-w-2xl text-sm text-muted" data-testid="sec-restricted">{{ t('artifact.security.restricted') }}</p>
      <StateBlock v-else-if="error" variant="error" :title="t('artifact.security.loadFailed')" :hint="error">
        <template #action><AppButton variant="secondary" @click="load">{{ t('ui.state.retry') }}</AppButton></template>
      </StateBlock>
      <p v-else-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</p>
      <p v-else-if="scans.length === 0" class="max-w-2xl text-sm text-muted" data-testid="sec-no-scans">{{ t('artifact.security.noScans') }}</p>
      <div v-else class="space-y-6">
        <div v-for="scan in scans" :key="scan.id">
          <h3 v-if="scans.length > 1" class="mb-2 font-mono text-xs text-muted">{{ scan.package_name }} · {{ scan.scanner }}</h3>
          <p v-if="scan.scan_status === 'failed' || scan.scan_status === 'partial'" role="status" class="mb-2 text-sm text-warning">{{ scan.scan_error || t('artifact.security.scanFailed') }}</p>
          <VulnScanFindingsTable :scan="scan" :vex-by-vuln="vexByScan[scan.artifact_id] ?? new Map()" @decide="decide(scan, $event)" />
        </div>
      </div>
    </section>

    <VexDecisionDialog
      v-if="deciding"
      :open="true"
      :repo="ctx.repoName.value"
      :artifact-id="deciding.scan.artifact_id"
      :artifact-label="`${ctx.pkgName.value}@${ctx.entry.value?.version ?? ''}`"
      :vuln-id="deciding.vulnId"
      :existing="vexByScan[deciding.scan.artifact_id]?.get(deciding.vulnId) ?? null"
      @close="deciding = null"
      @saved="afterDecision"
      @deleted="afterDecision"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppButton from '../AppButton.vue'
import StateBlock from '../ui/StateBlock.vue'
import SeverityBadge from '../ui/SeverityBadge.vue'
import VexDecisionDialog from '../VexDecisionDialog.vue'
import VulnScanFindingsTable from '../VulnScanFindingsTable.vue'
import { getVulnScan, listVulnScans, type VulnScan } from '../../api/security'
import { listVexStatements, type VexRecord } from '../../api/vex'
import { useArtifactContext } from '../../composables/useArtifact'
import { securityLevel } from '../../lib/artifactSecurity'
import { formatDateTime } from '../../lib/datetime'
import { vexByVulnId } from '../../lib/vex'
import { t, tp } from '../../i18n/en'

const SEVERITIES = ['critical', 'high', 'medium', 'low'] as const

const ctx = useArtifactContext()
const summary = computed(() => ctx.summary.value)
const level = computed(() => securityLevel(summary.value))

function scanLink(type: 'license' | 'secret') {
  return { path: '/security', query: { type, repo: ctx.repoName.value } }
}

const scans = ref<VulnScan[]>([])
const vexByScan = ref<Record<string, Map<string, VexRecord>>>({})
const loading = ref(false)
const error = ref('')
const deciding = ref<{ scan: VulnScan; vulnId: string } | null>(null)

let generation = 0
async function load(): Promise<void> {
  const mine = ++generation
  scans.value = []
  vexByScan.value = {}
  error.value = ''
  const entry = ctx.entry.value
  if (!ctx.caps.value.maintain || !entry) return
  loading.value = true
  try {
    const ids = new Set(entry.files.map((f) => f.id))
    const listed = (await listVulnScans(ctx.repoName.value)).filter((s) => ids.has(s.artifact_id))
    const [full, vex] = await Promise.all([
      Promise.all(listed.map((s) => getVulnScan(ctx.repoName.value, s.id))),
      Promise.all(listed.map((s) => listVexStatements(ctx.repoName.value, s.artifact_id))),
    ])
    if (mine !== generation) return
    scans.value = full
    vexByScan.value = Object.fromEntries(listed.map((s, i) => [s.artifact_id, vexByVulnId(vex[i] ?? [])]))
  } catch (err) {
    if (mine === generation) error.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (mine === generation) loading.value = false
  }
}

function decide(scan: VulnScan, vulnId: string): void {
  deciding.value = { scan, vulnId }
}

async function afterDecision(): Promise<void> {
  deciding.value = null
  await load()
}

watch(() => [ctx.entry.value?.version, ctx.caps.value.maintain, ctx.summary.value?.scanned_at], () => void load(), { immediate: true })
</script>
