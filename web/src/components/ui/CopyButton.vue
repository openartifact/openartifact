<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <button
    type="button"
    class="inline-flex h-9 min-w-9 shrink-0 items-center justify-center gap-1.5 rounded-md px-2 text-muted transition-colors hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:min-h-11 max-md:min-w-11"
    :aria-label="label ?? t('ui.copy.label')"
    @click="copy"
  >
    <component :is="done ? CheckIcon : ClipboardDocumentIcon" class="h-4 w-4" :class="done ? 'text-success' : ''" aria-hidden="true" />
    <span v-if="$slots.default" class="text-xs font-medium"><slot /></span>
  </button>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { CheckIcon, ClipboardDocumentIcon } from '@heroicons/vue/24/outline'
import { useToastStore } from '../../stores/toast'
import { t } from '../../i18n'

const props = defineProps<{
  text: string
  /** Accessible name; defaults to "Copy to clipboard". */
  label?: string
  /** Success toast title; defaults to "Copied to clipboard". */
  message?: string
}>()

const toasts = useToastStore()
const done = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
onBeforeUnmount(() => clearTimeout(timer))

async function write(value: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value)
      return true
    }
  } catch {
    // fall through to the selection fallback
  }
  const ta = document.createElement('textarea')
  ta.value = value
  ta.setAttribute('readonly', '')
  ta.style.cssText = 'position:fixed;opacity:0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  ta.remove()
  return ok
}

// The one feedback path: a toast (the toast host is the page's polite live
// region, so screen readers hear it) plus a brief check mark on the button.
async function copy(): Promise<void> {
  if (await write(props.text)) {
    toasts.success(props.message ?? t('ui.copy.done'))
    done.value = true
    clearTimeout(timer)
    timer = setTimeout(() => (done.value = false), 1600)
  } else {
    toasts.error(t('ui.copy.failed'), t('ui.copy.failedHint'))
  }
}
</script>
