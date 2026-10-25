<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <AdminDialog
    :open="show"
    drawer
    max-width="max-w-md"
    :title="t('sec.vuln.drawerTitle')"
    :description="tp('sec.vuln.poweredBy', { scanners: selectedScannerLabels })"
    @close="show = false"
  >
    <div class="space-y-4">
      <div>
        <label for="vuln-repo" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.vuln.drawerRepo') }}</label>
        <select
          id="vuln-repo"
          v-model="repo"
          class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
        >
          <option value="" disabled>{{ t('sec.common.selectRepo') }}</option>
          <option v-for="r in repos" :key="r.name" :value="r.name">{{ r.name }}</option>
        </select>
      </div>
      <fieldset>
        <legend class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.vuln.scannersLabel') }}</legend>
        <div class="flex items-center gap-4 flex-wrap">
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
              :data-testid="`scanner-install-${u.name}`"
            >{{ t(`sec.vuln.installState.${u.install_state}`) }}.</span>
            {{ u.message || t('sec.vuln.scannerNotReady') }}
            <a
              v-if="safeDocsUrl(u.docs_url)"
              :href="safeDocsUrl(u.docs_url)"
              target="_blank"
              rel="noopener noreferrer"
              class="text-primary underline"
            >{{ t('sec.vuln.scannerSetupGuide') }}</a>
          </li>
        </ul>
        <p class="text-xs text-muted mt-1">
          {{ selectedScanners.length ? t('sec.vuln.scannersHint') : t('sec.vuln.scannersRequired') }}
        </p>
      </fieldset>
      <div>
        <label for="vuln-pkg" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.common.package') }}</label>
        <select
          id="vuln-pkg"
          v-model="form.artifactId"
          :disabled="!repo"
          class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
          @change="onPackageChange"
        >
          <option value="" disabled>{{ t('sec.common.selectPackage') }}</option>
          <option v-for="pkg in packages" :key="pkg.id" :value="pkg.id">{{ pkg.name }}@{{ pkg.version }}</option>
        </select>
        <p v-if="repo && !packages.length" class="text-xs text-muted mt-1">{{ t('sec.common.noPackages') }}</p>
      </div>
      <div>
        <label for="vuln-eco" class="block text-xs font-medium text-muted mb-1.5">{{ t('sec.vuln.col.ecosystem') }}</label>
        <select
          id="vuln-eco"
          v-model="form.ecosystem"
          class="w-full h-9 px-3 rounded-lg bg-surface-hover/40 border border-border-strong text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
        >
          <option value="" disabled>{{ t('sec.vuln.selectEcosystem') }}</option>
          <option v-for="e in VULN_ECOSYSTEMS" :key="e" :value="e">{{ formatLabel(e) }}</option>
        </select>
        <p class="text-xs text-muted mt-1">{{ t('sec.vuln.ecosystemHint') }}</p>
      </div>
    </div>
    <template #footer>
      <AppButton variant="secondary" size="md" @click="show = false">
        {{ t('common.cancel') }}
      </AppButton>
      <AppButton variant="primary" size="md" :loading="scanning" :disabled="scanning || !form.artifactId || !form.ecosystem || !selectedScanners.length" @click="startScan">
        <ShieldCheckIcon v-if="!(scanning)" class="w-3.5 h-3.5" aria-hidden="true" />
        {{ scanning ? t('sec.vuln.starting') : t('sec.vuln.start') }}
      </AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { ShieldCheckIcon } from '@heroicons/vue/24/outline'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import type { PackageItem, Repository } from '../api/repositories'
import { listVulnScanners, startVulnScan, VULN_ECOSYSTEMS, type VulnScanner, type VulnScannerStatus } from '../api/security'
import { formatLabel } from '../lib/formats'
import { normalizeScannerSelection } from '../lib/scanStatus'
import { VULN_SCANNER_OPTIONS, safeDocsUrl, vulnScannerLabel } from '../lib/vulnScanners'
import { useToastStore } from '../stores/toast'
import { t, tp } from '../i18n/en'

const props = defineProps<{
  repos: Repository[]
  /** Packages of the selected repository. */
  packages: PackageItem[]
  findPackage: (artifactId: string) => PackageItem | undefined
}>()

const emit = defineEmits<{ started: [] }>()

/** The repository to scan; the page owns it because it also loads its packages. */
const repo = defineModel<string>('repo', { required: true })

const toasts = useToastStore()
const show = ref(false)
const scanning = ref(false)
const selectedScanners = ref<VulnScanner[]>(['osv'])
const form = ref({ artifactId: '', ecosystem: '' })

const selectedScannerLabels = computed(() =>
  VULN_SCANNER_OPTIONS.filter((s) => selectedScanners.value.includes(s.value))
    .map((s) => s.label)
    .join(', '),
)

// Server-side readiness per scanner, loaded for the selected repository. A scanner
// without an entry (no repository yet, list not loaded or failed) stays selectable:
// the server still reports a failed scan with the reason.
const scannerStatuses = ref<VulnScannerStatus[]>([])

function scannerReady(name: VulnScanner): boolean {
  const status = scannerStatuses.value.find((x) => x.name === name)
  return !status || status.ready
}

const unavailableScanners = computed(() => scannerStatuses.value.filter((x) => !x.ready))

async function loadScannerStatuses(forRepo: string): Promise<void> {
  if (!forRepo) return
  try {
    const statuses = await listVulnScanners(forRepo)
    if (!show.value || forRepo !== repo.value) return
    scannerStatuses.value = statuses
    selectedScanners.value = selectedScanners.value.filter((name) => scannerReady(name))
  } catch {
    if (forRepo === repo.value) scannerStatuses.value = []
  }
}

/** Opens the dialog; `prefillRepo` (the page's repository filter) wins over the last choice. */
function open(prefillRepo: string): void {
  form.value = { artifactId: '', ecosystem: '' }
  selectedScanners.value = ['osv']
  scannerStatuses.value = []
  const repoChanges = !!prefillRepo && prefillRepo !== repo.value
  if (prefillRepo) repo.value = prefillRepo
  show.value = true
  // A changed repository is loaded by the watcher below.
  if (!repoChanges) void loadScannerStatuses(repo.value)
}

watch(repo, (next) => {
  form.value = { artifactId: '', ecosystem: '' }
  scannerStatuses.value = []
  if (show.value) void loadScannerStatuses(next)
})

function onPackageChange(): void {
  const fmt = props.findPackage(form.value.artifactId)?.format?.toLowerCase() ?? ''
  form.value.ecosystem = (VULN_ECOSYSTEMS as readonly string[]).includes(fmt) ? fmt : ''
}

async function startScan(): Promise<void> {
  const pkg = props.findPackage(form.value.artifactId)
  if (!pkg || !repo.value || scanning.value) return
  scanning.value = true
  try {
    await startVulnScan(repo.value, {
      artifact_id: pkg.id,
      package_name: pkg.name,
      package_version: pkg.version,
      ecosystem: form.value.ecosystem,
      scanners: normalizeScannerSelection(selectedScanners.value),
    })
    toasts.success(t('sec.vuln.started'))
    show.value = false
    emit('started')
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vuln.startFailed'))
  } finally {
    scanning.value = false
  }
}

defineExpose({ open })
</script>
