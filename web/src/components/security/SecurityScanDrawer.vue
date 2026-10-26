<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Run a scan from the inbox: pick the kind (vulnerabilities, licenses, secrets,
  AI analysis), the repository and the package, then the kind's own inputs.
  A license scan reports its policy violations here: the list endpoint does not carry them.
-->
<template>
  <Drawer :open="open" :title="t('security.scan.title')" :description="t('security.scan.description')" :busy="running" @close="emit('close')">
    <form id="security-scan-form" class="space-y-4" @submit.prevent="run">
      <fieldset>
        <legend class="mb-1.5 block text-xs font-medium text-muted">{{ t('security.scan.kind') }}</legend>
        <div class="flex flex-wrap gap-2" role="radiogroup">
          <label
            v-for="k in kinds"
            :key="k"
            class="inline-flex min-h-9 cursor-pointer items-center gap-2 rounded-md border px-3 text-sm max-md:min-h-11"
            :class="kind === k ? 'border-primary bg-primary-soft text-foreground' : 'border-border text-muted hover:text-foreground'"
          >
            <input v-model="kind" type="radio" name="scan-kind" :value="k" class="sr-only" :data-testid="`scan-kind-${k}`" />
            {{ tDynamic(`security.type.${k}`) }}
          </label>
        </div>
      </fieldset>

      <div>
        <label for="scan-repo" class="mb-1.5 block text-xs font-medium text-muted">{{ t('security.vuln.drawerRepo') }}</label>
        <select id="scan-repo" v-model="selectedRepo" :class="SELECT" data-testid="scan-repo">
          <option value="" disabled>{{ t('security.common.selectRepo') }}</option>
          <option v-for="r in repos" :key="r.name" :value="r.name">{{ r.name }}</option>
        </select>
        <p v-if="reposLoaded && !repos.length" class="mt-1 text-xs text-muted">{{ t('security.common.noReposDesc') }}</p>
      </div>

      <div>
        <label for="scan-pkg" class="mb-1.5 block text-xs font-medium text-muted">{{ t('security.common.package') }}</label>
        <select id="scan-pkg" v-model="artifactId" :disabled="!selectedRepo" :class="SELECT" data-testid="scan-package" @change="onPackageChange">
          <option value="" disabled>{{ t('security.common.selectPackage') }}</option>
          <option v-for="pkg in packages" :key="pkg.id" :value="pkg.id">{{ pkg.name }}@{{ pkg.version }}</option>
        </select>
        <p v-if="selectedRepo && !packages.length" class="mt-1 text-xs text-muted">{{ t('security.common.noPackages') }}</p>
      </div>

      <template v-if="kind === 'vuln'">
        <fieldset>
          <legend class="mb-1.5 block text-xs font-medium text-muted">{{ t('security.vuln.scannersLabel') }}</legend>
          <div class="flex flex-wrap items-center gap-4">
            <label
              v-for="s in VULN_SCANNER_OPTIONS"
              :key="s.value"
              class="inline-flex items-center gap-2 text-sm"
              :class="scannerReady(s.value) ? 'text-foreground' : 'text-muted'"
            >
              <input
                v-model="selectedScanners"
                type="checkbox"
                :value="s.value"
                :disabled="!scannerReady(s.value)"
                :data-testid="`scanner-${s.value}`"
                class="h-4 w-4 rounded border-border-strong accent-primary"
              />
              {{ s.label }}
            </label>
          </div>
          <ul v-if="unavailableScanners.length" class="mt-2 space-y-1.5" data-testid="scanner-unavailable">
            <li v-for="u in unavailableScanners" :key="u.name" class="text-xs text-muted" :data-testid="`scanner-note-${u.name}`">
              <span class="font-medium text-foreground">{{ vulnScannerLabel(u.name) }}</span>:
              <span
                v-if="u.install_state === 'installing' || u.install_state === 'failed'"
                class="font-medium"
                :class="u.install_state === 'failed' ? 'text-danger' : 'text-foreground'"
              >{{ tDynamic(`security.vuln.installState.${u.install_state}`) }}.</span>
              {{ u.message || t('security.vuln.scannerNotReady') }}
              <a v-if="safeDocsUrl(u.docs_url)" :href="safeDocsUrl(u.docs_url)" target="_blank" rel="noopener noreferrer" class="text-primary underline">{{ t('security.vuln.scannerSetupGuide') }}</a>
            </li>
          </ul>
          <p class="mt-1 text-xs text-muted">{{ selectedScanners.length ? t('security.vuln.scannersHint') : t('security.vuln.scannersRequired') }}</p>
        </fieldset>
        <div>
          <label for="scan-eco" class="mb-1.5 block text-xs font-medium text-muted">{{ t('security.vuln.col.ecosystem') }}</label>
          <select id="scan-eco" v-model="ecosystem" :class="SELECT">
            <option value="" disabled>{{ t('security.vuln.selectEcosystem') }}</option>
            <option v-for="e in VULN_ECOSYSTEMS" :key="e" :value="e">{{ formatLabel(e) }}</option>
          </select>
          <p class="mt-1 text-xs text-muted">{{ t('security.vuln.ecosystemHint') }}</p>
        </div>
      </template>

      <p v-else-if="kind === 'license'" class="text-xs text-muted">{{ t('security.license.triggerHint') }}</p>

      <div v-else-if="kind === 'secret'">
        <label for="scan-content" class="mb-1.5 block text-xs font-medium text-muted">{{ t('security.secret.content') }}</label>
        <textarea
          id="scan-content"
          v-model="content"
          rows="8"
          spellcheck="false"
          :placeholder="t('security.secret.contentPlaceholder')"
          class="w-full resize-none rounded-lg border border-border-strong bg-surface-hover/40 px-3 py-2 font-mono text-sm text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
        <p class="mt-1 text-xs text-muted">{{ t('security.secret.contentHint') }}</p>
      </div>

      <div v-else>
        <label for="scan-manifest" class="mb-1.5 block text-xs font-medium text-muted">
          {{ t('security.ai.manifest') }} <span class="font-normal">({{ t('security.ai.optional') }})</span>
        </label>
        <textarea
          id="scan-manifest"
          v-model="manifest"
          rows="8"
          spellcheck="false"
          :placeholder="t('security.ai.manifestPlaceholder')"
          class="w-full resize-none rounded-lg border border-border-strong bg-surface-hover/40 px-3 py-2 font-mono text-sm text-foreground placeholder:text-muted focus:outline-none focus:ring-2 focus:ring-primary/50"
        />
        <p class="mt-1 text-xs text-muted">{{ t('security.ai.triggerHint') }}</p>
      </div>

      <div v-if="violations.length" data-testid="scan-violations">
        <h3 class="mb-2 text-sm font-semibold">{{ t('security.license.violations') }}</h3>
        <LicensePolicyViolations :violations="violations" />
      </div>
    </form>

    <template #footer>
      <AppButton variant="secondary" @click="emit('close')">{{ violations.length ? t('common.close') : t('common.cancel') }}</AppButton>
      <AppButton variant="primary" type="submit" form="security-scan-form" :loading="running" :disabled="!canRun" data-testid="scan-run">
        <ShieldCheckIcon v-if="!running" class="h-4 w-4" aria-hidden="true" />
        {{ running ? t('security.scan.running') : t('security.scan.run') }}
      </AppButton>
    </template>
  </Drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ShieldCheckIcon } from '@heroicons/vue/24/outline'
import AppButton from '../AppButton.vue'
import LicensePolicyViolations from '../LicensePolicyViolations.vue'
import Drawer from '../ui/Drawer.vue'
import { triggerAIAnalysis } from '../../api/ai'
import {
  listVulnScanners,
  startVulnScan,
  triggerLicenseScan,
  triggerSecretScan,
  VULN_ECOSYSTEMS,
  type FindingType,
  type LicensePolicyViolation,
  type VulnScanner,
  type VulnScannerStatus,
} from '../../api/security'
import { useRepoPackages } from '../../composables/useRepoPackages'
import { formatLabel } from '../../lib/formats'
import { normalizeScannerSelection } from '../../lib/scanStatus'
import { VULN_SCANNER_OPTIONS, safeDocsUrl, vulnScannerLabel } from '../../lib/vulnScanners'
import { useToastStore } from '../../stores/toast'
import { t, tDynamic } from '../../i18n/en'

const SELECT =
  'h-9 w-full rounded-lg border border-border-strong bg-surface-hover/40 px-3 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50 max-md:h-11'

const props = defineProps<{
  open: boolean
  /** Repository the inbox is filtered on; preselected. */
  repo: string
  /** Kind preselected (the inbox's type switch). */
  initialKind: FindingType
  /** AI analysis is only offered when the server has it on. `undefined`: unknown, offered. */
  aiEnabled?: boolean
}>()

const emit = defineEmits<{ close: []; started: [] }>()

const toasts = useToastStore()
const { repos, reposLoaded, selectedRepo, packages, findPackage } = useRepoPackages()

const kind = ref<FindingType>('vuln')
const artifactId = ref('')
const ecosystem = ref('')
const selectedScanners = ref<VulnScanner[]>(['osv'])
const scannerStatuses = ref<VulnScannerStatus[]>([])
const content = ref('')
const manifest = ref('')
const violations = ref<LicensePolicyViolation[]>([])
const running = ref(false)

const kinds = computed<FindingType[]>(() => (props.aiEnabled === false ? ['vuln', 'license', 'secret'] : ['vuln', 'license', 'secret', 'ai']))

const canRun = computed(() => {
  if (running.value || !selectedRepo.value || !artifactId.value) return false
  if (kind.value === 'vuln') return !!ecosystem.value && selectedScanners.value.length > 0
  if (kind.value === 'secret') return content.value.trim() !== ''
  return true
})

// A scanner without a status (not loaded or failed) stays selectable: the server reports a failed scan with the reason.
function scannerReady(name: VulnScanner): boolean {
  const status = scannerStatuses.value.find((x) => x.name === name)
  return !status || status.ready
}
const unavailableScanners = computed(() => scannerStatuses.value.filter((x) => !x.ready))

async function loadScannerStatuses(forRepo: string): Promise<void> {
  scannerStatuses.value = []
  if (!forRepo) return
  try {
    const statuses = await listVulnScanners(forRepo)
    if (forRepo !== selectedRepo.value) return
    scannerStatuses.value = statuses
    selectedScanners.value = selectedScanners.value.filter((name) => scannerReady(name))
  } catch {
    // Readiness is advice only.
  }
}

watch(
  () => props.open,
  (open) => {
    if (!open) return
    kind.value = props.initialKind === 'ai' && props.aiEnabled === false ? 'vuln' : props.initialKind
    artifactId.value = ''
    ecosystem.value = ''
    content.value = ''
    manifest.value = ''
    violations.value = []
    selectedScanners.value = ['osv']
    selectedRepo.value = props.repo
    void loadScannerStatuses(selectedRepo.value)
  },
  { immediate: true },
)

watch(selectedRepo, (next) => {
  artifactId.value = ''
  ecosystem.value = ''
  if (props.open) void loadScannerStatuses(next)
})

function onPackageChange(): void {
  const fmt = findPackage(artifactId.value)?.format?.toLowerCase() ?? ''
  ecosystem.value = (VULN_ECOSYSTEMS as readonly string[]).includes(fmt) ? fmt : ''
}

async function run(): Promise<void> {
  const pkg = findPackage(artifactId.value)
  if (!pkg || !canRun.value) return
  running.value = true
  try {
    const repo = selectedRepo.value
    switch (kind.value) {
      case 'vuln':
        await startVulnScan(repo, {
          artifact_id: pkg.id,
          package_name: pkg.name,
          package_version: pkg.version,
          ecosystem: ecosystem.value,
          scanners: normalizeScannerSelection(selectedScanners.value),
        })
        break
      case 'license': {
        const result = await triggerLicenseScan(repo, pkg.id)
        violations.value = result.policy_violations ?? []
        break
      }
      case 'secret':
        await triggerSecretScan(repo, pkg.id, content.value)
        break
      default:
        await triggerAIAnalysis(repo, pkg.id, {
          manifest_text: manifest.value || undefined,
          artifact_format: pkg.format,
          artifact_name: pkg.name,
          artifact_version: pkg.version,
        })
    }
    toasts.success(t('security.scan.started'))
    emit('started')
    // Policy violations are shown here, once; every other kind closes.
    if (!violations.value.length) emit('close')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('security.scan.failed'))
  } finally {
    running.value = false
  }
}
</script>
