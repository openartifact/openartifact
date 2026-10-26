<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="flex flex-wrap items-center gap-2" role="group" :aria-label="t('security.export.label')">
    <AppButton
      v-for="format in formats"
      :key="format"
      variant="secondary"
      :size="size"
      :disabled="disabled || busy !== null"
      :loading="busy === format"
      :data-testid="`export-${format}`"
      :aria-label="`${t('security.export.download')} ${tDynamic(`security.export.${format}`)}`"
      @click="download(format)"
    >
      <ArrowDownTrayIcon class="h-4 w-4" aria-hidden="true" />
      {{ tDynamic(`security.export.${format}`) }}
    </AppButton>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ArrowDownTrayIcon } from '@heroicons/vue/24/outline'
import AppButton from './AppButton.vue'
import { downloadScanExport } from '../api/security'
import { useToastStore } from '../stores/toast'
import { t, tDynamic } from '../i18n'

const props = withDefaults(
  defineProps<{
    /** Export formats offered, in order; each has a `security.export.<format>` label. */
    formats: readonly string[]
    /** Export route for a format. */
    url: (format: string) => string
    /** Name of the saved file when the server sends none. */
    fallbackName?: (format: string) => string
    disabled?: boolean
    size?: 'xs' | 'sm' | 'md'
  }>(),
  { fallbackName: (format: string) => `findings.${format === 'vex' ? 'vex.json' : format}`, disabled: false, size: 'md' },
)

const toasts = useToastStore()
const busy = ref<string | null>(null)

async function download(format: string): Promise<void> {
  if (busy.value) return
  busy.value = format
  try {
    await downloadScanExport(props.url(format), props.fallbackName(format))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('security.export.failed'))
  } finally {
    busy.value = null
  }
}
</script>
