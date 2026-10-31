// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createApp, watch } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './style.css'
import { useThemeStore } from './stores/theme'
import { useToastStore } from './stores/toast'
import { useBrandingStore } from './stores/branding'
import { useSessionStore } from './stores/session'
import { isSessionExpired } from './api/client'
import { initI18n, t } from './i18n'
import { captureInstallPrompt } from './composables/useInstallPrompt'
import { retireServiceWorkers, startServiceWorkerForServer } from './pwa/register'
import { loadAppVersion } from './composables/useAppVersion'
import { recordError, startTelemetry, stopTelemetry } from './telemetry'

// The catalog of the user's language is fetched before anything renders in it.
await initI18n()

const app = createApp(App)

app.use(createPinia())

// Initialize the theme store before the router mounts so the .dark class
// stays in sync with the persisted mode (the FOUC guard applied an initial
// value already from index.html).
useThemeStore().init()

// The server embeds the effective branding in index.html: apply it before the
// first render so the name, colours and icon never flash the defaults. The
// revalidation after mount picks up changes made since the page was served.
const branding = useBrandingStore()
branding.initFromDocument()

app.use(router)

// Global error handler: catches unhandled errors from components, watchers,
// lifecycle hooks and async setup. Shows a toast and logs to console.
app.config.errorHandler = (err, _instance, info) => {
  // An expired session already redirected to /login; a toast on top is noise.
  if (isSessionExpired(err)) return
  // Vue swallows these, so the telemetry error listener never sees them.
  recordError(err)
  const toast = useToastStore()
  const message = err instanceof Error ? err.message : String(err)
  toast.error(t('shell.unexpectedError'), message)
  console.error('[vue error]', info, err)
}

// Catch unhandled promise rejections that escape Vue's error boundary.
window.addEventListener('unhandledrejection', (event) => {
  if (isSessionExpired(event.reason)) return
  const toast = useToastStore()
  const message = event.reason instanceof Error ? event.reason.message : String(event.reason)
  toast.error(t('shell.unexpectedError'), message)
  console.error('[unhandledrejection]', event.reason)
})

// The install event can fire before any component mounts: listen from the start.
captureInstallPrompt()

// Experimental frontend telemetry: its settings need a session, so they are
// asked for once someone is signed in and stopped again on sign-out; nothing is loaded while the server has it off.
const session = useSessionStore()
watch(
  () => session.isAuthenticated && !session.mustChangePassword,
  (signedIn) => {
    if (signedIn) void startTelemetry()
    else stopTelemetry()
  },
  { immediate: true },
)

app.mount('#app')
void branding.refresh()

// Not in dev: Vite serves modules the worker would otherwise have to ignore, and a development
// server (an unstamped build) would offer an update after every rebuild. Both also drop a worker an
// earlier visit left behind.
if (import.meta.env.PROD) void startServiceWorkerForServer(loadAppVersion)
else void retireServiceWorkers()
