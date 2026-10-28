<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Admin card on the security page: state of the local OSV advisory mirror, one
  row per ecosystem, and the CC-BY 4.0 attribution the OSV data requires.
-->
<template>
  <section class="rounded-lg border border-border bg-surface p-4" data-testid="osv-mirror-card">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 class="text-sm font-semibold text-foreground">{{ t('security.osv.title') }}</h2>
      <span
        v-if="status"
        class="rounded border px-1.5 py-px text-caption"
        :class="status.enabled ? 'border-success/40 text-success' : 'border-border text-muted'"
        data-testid="osv-mirror-state"
      >{{ status.enabled ? t('security.osv.enabled') : t('security.osv.disabled') }}</span>
    </div>

    <p v-if="error" class="mt-2 text-sm text-danger" role="alert" data-testid="osv-mirror-error">{{ t('security.osv.loadFailed') }}</p>

    <template v-else-if="status">
      <p v-if="!status.enabled" class="mt-2 text-sm text-muted" data-testid="osv-mirror-disabled">{{ t('security.osv.disabledHint') }}</p>
      <template v-else>
        <p class="mt-2 text-sm text-muted">
          {{ tp('security.osv.source', { url: status.source_url }) }}
          {{ tp('security.osv.interval', { hours: String(Math.round(status.interval_seconds / 3600)) }) }}
        </p>
        <p v-if="status.ecosystems.length === 0" class="mt-2 text-sm text-muted">{{ t('security.osv.noEcosystems') }}</p>
        <div v-else class="mt-3 overflow-x-auto">
          <table class="w-full text-left text-sm" data-testid="osv-mirror-table">
            <caption class="sr-only">{{ t('security.osv.caption') }}</caption>
            <thead class="text-caption text-muted">
              <tr>
                <th scope="col" class="py-1 pr-4 font-medium">{{ t('security.osv.col.ecosystem') }}</th>
                <th scope="col" class="py-1 pr-4 font-medium">{{ t('security.osv.col.advisories') }}</th>
                <th scope="col" class="py-1 pr-4 font-medium">{{ t('security.osv.col.lastImport') }}</th>
                <th scope="col" class="py-1 pr-4 font-medium">{{ t('security.osv.col.lastRematch') }}</th>
                <th scope="col" class="py-1 font-medium">{{ t('security.osv.col.status') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in status.ecosystems" :key="e.ecosystem" class="border-t border-border" :data-testid="`osv-eco-${e.ecosystem}`">
                <td class="py-1.5 pr-4 font-medium text-foreground">{{ e.ecosystem }}</td>
                <td class="py-1.5 pr-4">{{ e.populated ? e.advisories.toLocaleString() : '—' }}</td>
                <td class="py-1.5 pr-4" :title="formatDateTime(e.last_imported_at)">
                  {{ e.last_imported_at ? timeAgo(e.last_imported_at) : t('security.osv.never') }}
                </td>
                <td class="py-1.5 pr-4">{{ e.populated ? tp('security.osv.seconds', { n: String(e.last_rematch_seconds) }) : '—' }}</td>
                <td class="py-1.5">
                  <span
                    v-if="e.last_error"
                    class="rounded border border-danger/40 px-1.5 py-px text-caption text-danger"
                    :title="e.last_error"
                    data-testid="osv-eco-error"
                  >{{ t('security.osv.status.error') }}</span>
                  <span v-else-if="!e.populated" class="text-muted">{{ t('security.osv.status.waiting') }}</span>
                  <span v-else-if="e.rematch_pending" class="text-warning">{{ t('security.osv.status.rematching') }}</span>
                  <span v-else class="text-success">{{ t('security.osv.status.ok') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
      <p class="mt-3 text-caption text-muted" data-testid="osv-attribution">
        <a :href="safeHref(status.attribution.url)" target="_blank" rel="noopener noreferrer" class="text-primary underline">{{ t('security.osv.attribution') }}</a>
      </p>
    </template>
  </section>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { getOsvMirrorStatus } from '../../api/security'
import type { OsvMirrorStatus } from '../../api/generated/types.gen'
import { formatDateTime, timeAgo } from '../../lib/datetime'
import { t, tp } from '../../i18n'

const status = ref<OsvMirrorStatus | null>(null)
const error = ref(false)

/** The attribution link comes from the server: only http(s) targets are rendered. */
const safeHref = (url: string): string => (/^https?:\/\//i.test(url) ? url : 'https://osv.dev')

onMounted(async () => {
  try {
    status.value = await getOsvMirrorStatus()
  } catch {
    error.value = true
  }
})
</script>
