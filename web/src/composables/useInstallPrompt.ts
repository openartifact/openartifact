// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref } from 'vue'

// Chromium-only event, absent from the DOM lib.
interface BeforeInstallPromptEvent extends Event {
  prompt(): Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

// The browser fires the event once, early and for the whole page: it is kept
// at module level so a component mounted later still finds it.
const deferred = ref<BeforeInstallPromptEvent | null>(null)

/** Starts listening for the install offer; call once at startup. Returns a stop function. */
export function captureInstallPrompt(target: Window = window): () => void {
  const onPrompt = (e: Event) => {
    e.preventDefault() // keep the native mini-infobar out; the app offers its own button
    deferred.value = e as BeforeInstallPromptEvent
  }
  const onInstalled = () => {
    deferred.value = null
  }
  target.addEventListener('beforeinstallprompt', onPrompt)
  target.addEventListener('appinstalled', onInstalled)

  return () => {
    target.removeEventListener('beforeinstallprompt', onPrompt)
    target.removeEventListener('appinstalled', onInstalled)
    deferred.value = null
  }
}

/** Install offer: `canInstall` is true only while the browser has offered one. */
export function useInstallPrompt() {
  async function install(): Promise<void> {
    const event = deferred.value
    if (!event) return
    // The event is single-use whatever the answer.
    deferred.value = null
    await event.prompt()
    await event.userChoice
  }

  return { canInstall: computed(() => deferred.value !== null), install }
}
