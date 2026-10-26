<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->

    <div v-if="loading" class="text-muted" role="status">{{ t('common.loading') }}</div>
    <div v-else-if="loadError" class="bg-surface rounded-xl border border-border py-12 text-center" role="alert">
      <p class="text-sm text-danger">{{ loadError }}</p>
      <AppButton variant="secondary" size="sm" class="mt-3" @click="load">{{ t('security.common.retry') }}</AppButton>
    </div>
    <UnavailableStub v-else-if="!sysInfo" />
    <div v-else class="space-y-5">
      <!-- Info Cards -->
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
        <!-- Build Card -->
        <div class="bg-surface rounded-xl border border-border p-5">
          <h3 class="text-sm font-semibold mb-3">{{ t('admin.system.build') }}</h3>
          <dl>
            <div
              v-for="(row, i) in buildRows"
              :key="row.label"
              :class="['flex items-center justify-between py-2', i < buildRows.length - 1 ? 'border-b border-border' : '']"
            >
              <dt class="text-sm text-muted">{{ row.label }}</dt>
              <dd v-if="row.value" class="text-sm font-mono text-foreground" :title="row.title">{{ row.value }}</dd>
              <dd v-else class="text-sm text-muted" :title="t('adm.system.notStampedHint')">{{ t('adm.system.notStamped') }}</dd>
            </div>
          </dl>
        </div>

        <!-- Database Card -->
        <div class="bg-surface rounded-xl border border-border p-5">
          <h3 class="text-sm font-semibold mb-3">{{ t('admin.system.database') }}</h3>
          <div class="flex items-center justify-between py-2">
            <span class="text-sm text-muted">{{ t('adm.system.status') }}</span>
            <div class="flex items-center gap-2">
              <span :class="['w-2 h-2 rounded-full shrink-0', dbHealthy ? 'bg-success' : 'bg-danger']" aria-hidden="true" />
              <span :class="['text-sm font-mono font-medium', dbHealthy ? 'text-success' : 'text-danger']">
                {{ sysInfo.db_status || '—' }}
              </span>
            </div>
          </div>
        </div>

        <!-- Storage Card -->
        <div class="bg-surface rounded-xl border border-border p-5">
          <h3 class="text-sm font-semibold mb-3">{{ t('admin.system.storage') }}</h3>
          <div class="flex items-center justify-between py-2 border-b border-border">
            <span class="text-sm text-muted">{{ t('adm.system.kind') }}</span>
            <span class="text-sm font-mono text-foreground">{{ sysInfo.storage_kind || '—' }}</span>
          </div>
          <div class="flex items-center justify-between py-2">
            <span class="text-sm text-muted">{{ t('adm.system.status') }}</span>
            <div class="flex items-center gap-2">
              <span :class="['w-2 h-2 rounded-full shrink-0', storageHealthy ? 'bg-success' : 'bg-danger']" aria-hidden="true" />
              <span :class="['text-sm font-mono font-medium', storageHealthy ? 'text-success' : 'text-danger']">
                {{ sysInfo.storage_status || '—' }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import AppButton from '../../../components/AppButton.vue'
import { ref, computed, onMounted } from 'vue'
import { getAdminSystemInfo, type AdminSystemInfo } from '../../../api/admin'
import UnavailableStub from '../../../components/UnavailableStub.vue'
import { formatBuildTime, formatCommit } from '../../../lib/buildInfo'
import { formatVersion } from '../../../composables/useAppVersion'
import { t } from '../../../i18n/en'

const sysInfo = ref<AdminSystemInfo | null>(null)
const loading = ref(false)
const loadError = ref('')

interface BuildRow {
  label: string
  /** Display value; null renders the "not stamped" label. */
  value: string | null
  /** Tooltip with the raw value when the display form is abbreviated. */
  title?: string
}

// Version, commit and build time are stamped at build time; an unstamped build
// reports "unknown" (or "dev" for the version), which is labelled, not shown as data.
const buildRows = computed<BuildRow[]>(() => {
  const info = sysInfo.value
  const commit = formatCommit(info?.commit)
  const buildTime = formatBuildTime(info?.build_time)
  return [
    { label: t('admin.system.version'), value: info?.version ? formatVersion(info.version) : null },
    { label: t('admin.system.uptime'), value: info?.uptime || null },
    { label: t('adm.system.commit'), value: commit, title: commit && info?.commit !== commit ? info?.commit : undefined },
    { label: t('adm.system.buildTime'), value: buildTime, title: buildTime ? info?.build_time : undefined },
    { label: t('admin.system.go'), value: info?.go_version || null },
    { label: t('adm.system.os'), value: info?.os || null },
    { label: t('adm.system.arch'), value: info?.arch || null },
  ]
})

const dbHealthy = computed(() => sysInfo.value?.db_status === 'ok')
const storageHealthy = computed(() => sysInfo.value?.storage_status === 'ok')

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    sysInfo.value = await getAdminSystemInfo()
  } catch (err) {
    sysInfo.value = null
    loadError.value = err instanceof Error ? err.message : t('adm.system.loadFailed')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>
