<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The notifications that stay until dismissed (today: "New version available"). They take a row between the
  header and the page, right-aligned to the content gutter, and push the page down instead of covering it: a
  toast that is still there a minute later must not sit on the card the user is reading. The row exists only
  while there is one. Timed toasts are floated by `ToastHost`.
-->
<template>
  <div
    v-if="toasts.length > 0"
    class="shrink-0 border-b border-border bg-bg px-4 py-3 md:px-8"
    role="region"
    aria-live="polite"
    :aria-label="t('shell.notifications')"
  >
    <div class="mx-auto flex max-w-[1400px] flex-col gap-2 md:items-end">
      <ToastCard v-for="toast in toasts" :key="toast.id" :toast="toast" @run="run(toast)" @dismiss="toastStore.dismiss(toast.id)" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import ToastCard from './ToastCard.vue'
import { isSticky, useToastStore, type Toast } from '../stores/toast'
import { t } from '../i18n'

const toastStore = useToastStore()
const toasts = computed(() => toastStore.visible.filter(isSticky))

function run(toast: Toast): void {
  toastStore.dismiss(toast.id)
  toast.action?.run()
}
</script>
