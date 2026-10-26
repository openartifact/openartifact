<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <button
    type="button"
    class="font-mono text-xs text-foreground hover:text-primary focus-visible:ring-2 focus-visible:ring-primary rounded"
    :title="sha"
    :aria-label="`${t('common.copy')} ${sha}`"
    @click="copy"
  >
    {{ sha.slice(0, 12) }}
  </button>
</template>

<script setup lang="ts">
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

const props = defineProps<{ sha: string }>()
const toasts = useToastStore()

async function copy(): Promise<void> {
  try {
    await navigator.clipboard.writeText(props.sha)
    toasts.success(t('common.copied'))
  } catch {
    // clipboard unavailable (insecure context): the full sha is in the tooltip
  }
}
</script>
