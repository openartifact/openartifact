<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Result of a cleanup dry run: what a run would remove, per repository. Nothing
  has been removed when this is shown.
-->
<template>
  <AdminDialog
    :open="open"
    max-width="max-w-2xl"
    :eyebrow="t('cleanup.dryRun.eyebrow')"
    :title="title"
    @close="emit('close')"
  >
    <p class="text-sm text-muted" data-testid="dryrun-total">
      {{ total === 0 ? t('cleanup.dryRun.none') : tp('cleanup.dryRun.total', { n: String(total) }) }}
    </p>
    <div class="mt-4 space-y-4 max-h-96 overflow-y-auto">
      <section v-for="repo in repositories" :key="repo.repository" data-testid="dryrun-repo">
        <h3 class="text-sm font-semibold text-foreground">
          <span class="font-mono">{{ repo.repository }}</span>
          <span class="text-muted font-normal"> · {{ repo.format }} · {{ repo.rows }}</span>
        </h3>
        <ul v-if="repo.items.length" class="mt-1 divide-y divide-border rounded-lg border border-border">
          <li v-for="it in repo.items" :key="it.id" class="px-3 py-1.5 text-sm font-mono text-muted" data-testid="dryrun-item">
            <span class="text-foreground">{{ it.name }}</span> {{ it.version }}
          </li>
        </ul>
        <p v-if="repo.truncated" class="mt-1 text-xs text-muted">{{ t('cleanup.dryRun.truncated') }}</p>
      </section>
    </div>
    <template #footer>
      <AppButton variant="ghost" @click="emit('close')">{{ t('common.close') }}</AppButton>
    </template>
  </AdminDialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import AdminDialog from './AdminDialog.vue'
import AppButton from './AppButton.vue'
import type { CleanupPreview } from '../api/generated'
import { t, tp } from '../i18n/en'

const props = defineProps<{ open: boolean; title: string; repositories: CleanupPreview[] }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const total = computed(() => props.repositories.reduce((n, r) => n + r.rows, 0))
</script>
