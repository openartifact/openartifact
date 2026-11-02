<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  What a page load does for a screen reader, which a single-page app does not do by itself: say where you are.
  On every change of route path (not of the query: a tab or a filter is not a new page) the new page title is
  put into a status region that is always in the DOM, and when the element that held focus went with the old
  page (a repository link in a table, a row action) focus moves to the main area, so the next Tab starts in the
  new page instead of at the top of the document. Focus that is still somewhere sensible (the sidebar link just
  used) stays.
-->
<template>
  <p class="sr-only" role="status" aria-live="polite" aria-atomic="true" data-testid="route-announcer">{{ announcement }}</p>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref } from 'vue'
import { START_LOCATION, useRouter } from 'vue-router'
import { useBrandingStore } from '../stores/branding'

const router = useRouter()
const branding = useBrandingStore()
const announcement = ref('')

// The first navigation is the page load itself: the browser announces that and the skip link must stay the first Tab stop.
const stop = router.afterEach(async (to, from, failure) => {
  if (failure || from === START_LOCATION || to.path === from.path) return
  // The title is set by another afterEach hook, before the new page renders.
  await nextTick()
  announcement.value = branding.pageTitle
  const active = document.activeElement
  if (!active || active === document.body || !active.isConnected) document.getElementById('main-content')?.focus({ preventScroll: true })
})
onBeforeUnmount(stop)
</script>
