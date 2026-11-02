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
    :aria-label="t('shell.notifications')"
  >
    <!-- Two live regions that exist before any toast does (a region added together with its text is not announced):
         errors and warnings interrupt (assertive; not `role="alert"`, which would be a second alert on every page), the rest wait for the screen reader to finish (`status`). -->
    <div class="flex flex-col-reverse gap-2 md:items-end" aria-live="assertive" aria-atomic="false" data-testid="toast-urgent">
      <TransitionGroup v-bind="transition">
        <ToastCard v-for="toast in urgent" :key="toast.id" :toast="toast" @run="run(toast)" @dismiss="toastStore.dismiss(toast.id)" @hold="toastStore.pause(toast.id)" @release="toastStore.resume(toast.id)" />
      </TransitionGroup>
    </div>
    <div class="flex flex-col-reverse gap-2 md:items-end" role="status">
      <TransitionGroup v-bind="transition">
        <ToastCard v-for="toast in polite" :key="toast.id" :toast="toast" @run="run(toast)" @dismiss="toastStore.dismiss(toast.id)" @hold="toastStore.pause(toast.id)" @release="toastStore.resume(toast.id)" />
      </TransitionGroup>
    </div>
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
const urgent = computed(() => toasts.value.filter((toast) => toast.level === 'error' || toast.level === 'warning'))
const polite = computed(() => toasts.value.filter((toast) => toast.level !== 'error' && toast.level !== 'warning'))

const transition = {
  enterActiveClass: 'transition-all duration-300 ease-out',
  enterFromClass: 'opacity-0 translate-y-3',
  enterToClass: 'opacity-100 translate-y-0',
  leaveActiveClass: 'transition-all duration-200 ease-in',
  leaveFromClass: 'opacity-100 translate-y-0',
  leaveToClass: 'opacity-0 translate-y-3',
}

function run(toast: Toast): void {
  toastStore.dismiss(toast.id)
  toast.action?.run()
}
</script>
