<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Live preview in the cleanup policy editor: what the policy being edited would
  delete right now (rows, size, a sample). The server only evaluates it; nothing
  is deleted or saved.
-->
<template>
  <section class="rounded-lg border border-border p-3.5" :aria-label="t('cleanup.preview.title')" data-testid="cleanup-live-preview">
    <h3 class="text-xs font-semibold text-foreground">{{ t('cleanup.preview.title') }}</h3>
    <p class="mt-0.5 text-caption text-muted" data-testid="cleanup-preview-safe">{{ t('cleanup.preview.safe') }}</p>

    <div class="mt-3" aria-live="polite">
      <p v-if="status === 'idle'" class="text-sm text-muted" data-testid="cleanup-preview-idle">{{ t('cleanup.preview.idle') }}</p>

      <StateBlock
        v-else-if="status === 'error'"
        variant="error"
        :title="t('cleanup.dryRun.failed')"
        :hint="error"
        data-testid="cleanup-preview-error"
      />

      <p v-else-if="!result" class="text-sm text-muted" data-testid="cleanup-preview-loading">{{ t('cleanup.preview.loading') }}</p>

      <div v-else :class="status === 'loading' ? 'opacity-60' : undefined" :aria-busy="status === 'loading'">
        <p v-if="result.rows === 0" class="text-sm text-foreground" data-testid="cleanup-preview-none">
          {{ t('cleanup.preview.none') }}
        </p>
        <template v-else>
          <dl class="grid grid-cols-2 gap-3">
            <div class="rounded-lg border border-border p-3">
              <dt class="text-caption text-muted">{{ t('cleanup.preview.rows') }}</dt>
              <dd class="text-xl font-bold tabular-nums text-foreground" data-testid="cleanup-preview-rows">
                {{ formatNumber(result.rows) }}
              </dd>
            </div>
            <div class="rounded-lg border border-border p-3">
              <dt class="text-caption text-muted">{{ t('cleanup.preview.size') }}</dt>
              <dd class="text-xl font-bold tabular-nums text-foreground" data-testid="cleanup-preview-bytes">
                {{ formatBytes(result.bytes) }}
              </dd>
            </div>
          </dl>
          <ul class="mt-3 divide-y divide-border rounded-lg border border-border" data-testid="cleanup-preview-sample">
            <li
              v-for="it in sample"
              :key="it.id"
              class="flex items-baseline gap-2 px-3 py-1.5 text-sm font-mono text-muted"
              data-testid="cleanup-preview-item"
            >
              <span class="min-w-0 grow truncate text-foreground" :title="it.name">{{ it.name }}</span>
              <span class="shrink-0">{{ it.version }}</span>
            </li>
          </ul>
          <p v-if="result.rows > sample.length" class="mt-1 text-xs text-muted" data-testid="cleanup-preview-more">
            {{ result.truncated ? t('cleanup.dryRun.truncated') : tp('cleanup.preview.more', { n: formatNumber(result.rows - sample.length) }) }}
          </p>
        </template>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import StateBlock from './ui/StateBlock.vue'
import type { CleanupPreview } from '../api/cleanup'
import type { LivePreviewStatus } from '../composables/useCleanupLivePreview'
import { t, tp } from '../i18n'
import { formatNumber } from '../lib/intl'
import { formatBytes } from '../lib/quota'

/** Rows listed in the sample; the count above is the whole result. */
const SAMPLE_ROWS = 8

const props = defineProps<{ status: LivePreviewStatus; result: CleanupPreview | null; error: string }>()

const sample = computed(() => props.result?.items.slice(0, SAMPLE_ROWS) ?? [])
</script>
