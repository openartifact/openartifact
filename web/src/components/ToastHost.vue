<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The timed notifications (saved, copied, failed), floated over the page for a few seconds in the bottom-right
  corner (full width at the bottom below md, in the thumb's reach). They are gone before they can matter, and the
  corner holds the least: the top-right is where pages put their own actions (Refresh, New repository). A toast
  that stays until dismissed does not float here: `ToastBanner` docks it in the page so it covers nothing.
-->
<template>
  <div
    class="pointer-events-none fixed inset-x-4 bottom-4 z-50 flex flex-col-reverse gap-2 md:inset-x-auto md:right-8 md:items-end"
    role="region"
    aria-live="polite"
    :aria-label="t('shell.notifications')"
  >
    <TransitionGroup
      enter-active-class="transition-all duration-300 ease-out"
      enter-from-class="opacity-0 translate-y-3"
      enter-to-class="opacity-100 translate-y-0"
      leave-active-class="transition-all duration-200 ease-in"
      leave-from-class="opacity-100 translate-y-0"
      leave-to-class="opacity-0 translate-y-3"
    >
      <ToastCard v-for="toast in toasts" :key="toast.id" :toast="toast" @run="run(toast)" @dismiss="toastStore.dismiss(toast.id)" />
    </TransitionGroup>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { TransitionGroup } from 'vue'
import ToastCard from './ToastCard.vue'
import { isSticky, useToastStore, type Toast } from '../stores/toast'
import { t } from '../i18n'

const toastStore = useToastStore()
const toasts = computed(() => toastStore.visible.filter((toast) => !isSticky(toast)))

function run(toast: Toast): void {
  toastStore.dismiss(toast.id)
  toast.action?.run()
}
</script>
