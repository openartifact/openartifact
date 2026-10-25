// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './style.css'
import { useThemeStore } from './stores/theme'
import { useToastStore } from './stores/toast'
import { useBrandingStore } from './stores/branding'
import { isSessionExpired } from './api/client'
import { t } from './i18n/en'
import { captureInstallPrompt } from './composables/useInstallPrompt'
import { startServiceWorker } from './pwa/register'

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

app.mount('#app')
void branding.refresh()

// Not in dev: Vite serves modules the worker would otherwise have to ignore.
if (import.meta.env.PROD) startServiceWorker()
