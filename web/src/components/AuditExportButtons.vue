<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Download buttons of an audit export: one per wire format. `download` starts
  the transfer for the chosen format; a refusal is reported as a toast.
-->
<template>
  <div class="flex items-center gap-2">
    <AppButton
      v-for="f in formats"
      :key="f"
      variant="secondary"
      size="sm"
      :loading="busy === f"
      :disabled="busy !== null"
      :data-testid="`export-${f}`"
      @click="run(f)"
    >
      <ArrowDownTrayIcon class="w-4 h-4" aria-hidden="true" />
      {{ t(`common.export.${f}`) }}
    </AppButton>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ArrowDownTrayIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import type { ExportFormat } from '../api/audit'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const props = defineProps<{ download: (format: ExportFormat) => Promise<void> }>()

const formats: ExportFormat[] = ['jsonl', 'csv']
const toasts = useToastStore()
const busy = ref<ExportFormat | null>(null)

async function run(format: ExportFormat): Promise<void> {
  if (busy.value) return
  busy.value = format
  try {
    await props.download(format)
  } catch (err) {
    toasts.error(t('common.export.failed'), err instanceof Error ? err.message : undefined)
  } finally {
    busy.value = null
  }
}
</script>
