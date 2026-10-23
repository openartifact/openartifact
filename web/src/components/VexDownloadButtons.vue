<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="flex items-center gap-2" role="group" :aria-label="t('sec.vex.download.label')">
    <AppButton
      v-for="format in VEX_DOCUMENT_FORMATS"
      :key="format"
      variant="secondary"
      :size="size"
      :disabled="busy !== null"
      :loading="busy === format"
      :data-testid="`${testIdPrefix}-${format}`"
      :aria-label="`${t('sec.export.download')} ${tDynamic(`sec.vex.download.${format}`)}`"
      @click="download(format)"
    >
      <ArrowDownTrayIcon class="w-4 h-4" aria-hidden="true" />
      {{ tDynamic(`sec.vex.download.${format}`) }}
    </AppButton>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ArrowDownTrayIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import { downloadVexDocument, VEX_DOCUMENT_FORMATS, type VexDocumentFormat } from '../api/vex'
import { useToastStore } from '../stores/toast'
import { t, tDynamic } from '../i18n/en'

const props = withDefaults(
  defineProps<{
    repo: string
    /** Limits the document to one artifact; otherwise it covers every scanned artifact of the repository. */
    artifactId?: string
    /** `data-testid` of each button is `<prefix>-<format>`. */
    testIdPrefix?: string
    size?: 'xs' | 'sm' | 'md'
  }>(),
  { artifactId: undefined, testIdPrefix: 'vex-download', size: 'md' },
)

const toasts = useToastStore()
const busy = ref<VexDocumentFormat | null>(null)

async function download(format: VexDocumentFormat): Promise<void> {
  if (busy.value) return
  busy.value = format
  try {
    await downloadVexDocument(props.repo, format, { artifactId: props.artifactId })
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('sec.vex.download.failed'))
  } finally {
    busy.value = null
  }
}
</script>
