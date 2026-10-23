<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="flex items-center gap-2" role="group" :aria-label="t('sec.export.label')">
    <AppButton
      v-for="format in SCAN_EXPORT_FORMATS"
      :key="format"
      variant="secondary"
      size="md"
      :disabled="disabled || busy !== null"
      :loading="busy === format"
      :data-testid="`export-${format}`"
      :aria-label="`${t('sec.export.download')} ${tDynamic(`sec.export.${format}`)}`"
      @click="download(format)"
    >
      <ArrowDownTrayIcon class="w-4 h-4" aria-hidden="true" />
      {{ tDynamic(`sec.export.${format}`) }}
    </AppButton>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ArrowDownTrayIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import { downloadScanExport, SCAN_EXPORT_FORMATS, type ScanExportFormat } from '../api/security'
import { useToastStore } from '../stores/toast'
import { t, tDynamic } from '../i18n/en'

const props = defineProps<{
  /** Export route for a format. */
  url: (format: ScanExportFormat) => string
  disabled?: boolean
}>()

const toasts = useToastStore()
const busy = ref<ScanExportFormat | null>(null)

async function download(format: ScanExportFormat): Promise<void> {
  if (busy.value) return
  busy.value = format
  try {
    await downloadScanExport(props.url(format), format)
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.export.failed'))
  } finally {
    busy.value = null
  }
}
</script>
