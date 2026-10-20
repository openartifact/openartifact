<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- PageHead -->
    <div class="flex items-start justify-between flex-wrap gap-3">
      <div>
        <div class="flex items-center gap-2 text-[11px] uppercase tracking-[0.14em] text-muted font-mono">
          <span>{{ t('nav.admin') }}</span>
          <ChevronRightIcon class="w-3 h-3" aria-hidden="true" />
          <span class="text-foreground/70">{{ t('nav.system') }}</span>
        </div>
        <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('admin.system.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('adm.system.description') }}</p>
      </div>
    </div>

    <div v-if="loading" class="text-muted" role="status">{{ t('common.loading') }}</div>
    <div v-else-if="loadError" class="bg-surface rounded-xl border border-border py-12 text-center" role="alert">
      <p class="text-sm text-danger">{{ loadError }}</p>
      <button type="button" class="mt-3 px-3 py-1.5 rounded-lg text-sm border border-border hover:bg-surface-hover" @click="load">
        {{ t('sec.common.retry') }}
      </button>
    </div>
    <UnavailableStub v-else-if="!sysInfo" />
    <div v-else class="space-y-5">
      <!-- Stat Row: Version · Go Version · OS/Arch · Uptime -->
      <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
        <div v-for="stat in statTiles" :key="stat.label" class="bg-surface rounded-xl border border-border p-4 flex items-center gap-3">
          <span class="w-9 h-9 rounded-lg flex items-center justify-center text-primary bg-primary-soft shrink-0">
            <component :is="stat.icon" class="w-4 h-4" aria-hidden="true" />
          </span>
          <div class="min-w-0">
            <div class="text-[10px] uppercase tracking-[0.14em] font-semibold text-muted">{{ stat.label }}</div>
            <div class="text-lg font-bold font-mono truncate">{{ stat.value }}</div>
          </div>
        </div>
      </div>

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
              <dd class="text-sm font-mono text-foreground">{{ row.value }}</dd>
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
import { ref, computed, onMounted } from 'vue'
import { getAdminSystemInfo, type AdminSystemInfo } from '../../api/admin'
import UnavailableStub from '../../components/UnavailableStub.vue'
import { t } from '../../i18n/en'
import { ChevronRightIcon, CodeBracketIcon, ClockIcon, ServerIcon } from '@heroicons/vue/24/outline'

const sysInfo = ref<AdminSystemInfo | null>(null)
const loading = ref(false)
const loadError = ref('')

const osArch = computed(() => {
  const { os, arch } = sysInfo.value ?? {}
  if (os && arch) return `${os}/${arch}`
  return os || arch || '—'
})

const statTiles = computed(() => [
  { label: t('admin.system.version'), value: sysInfo.value?.version || '—', icon: CodeBracketIcon },
  { label: t('admin.system.go'), value: sysInfo.value?.go_version || '—', icon: CodeBracketIcon },
  { label: t('adm.system.osArch'), value: osArch.value, icon: ServerIcon },
  { label: t('admin.system.uptime'), value: sysInfo.value?.uptime || '—', icon: ClockIcon },
])

const buildRows = computed(() => [
  { label: t('admin.system.version'), value: sysInfo.value?.version || '—' },
  { label: t('adm.system.commit'), value: sysInfo.value?.commit || '—' },
  { label: t('adm.system.buildTime'), value: sysInfo.value?.build_time || '—' },
  { label: t('admin.system.go'), value: sysInfo.value?.go_version || '—' },
  { label: t('adm.system.os'), value: sysInfo.value?.os || '—' },
  { label: t('adm.system.arch'), value: sysInfo.value?.arch || '—' },
])

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
