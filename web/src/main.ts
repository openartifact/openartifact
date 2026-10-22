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
import { isSessionExpired } from './api/client'

const app = createApp(App)

app.use(createPinia())

// Initialize the theme store before the router mounts so the .dark class
// stays in sync with the persisted mode (the FOUC guard applied an initial
// value already from index.html).
useThemeStore().init()

app.use(router)

// Global error handler: catches unhandled errors from components, watchers,
// lifecycle hooks and async setup. Shows a toast and logs to console.
app.config.errorHandler = (err, _instance, info) => {
  // An expired session already redirected to /login; a toast on top is noise.
  if (isSessionExpired(err)) return
  const toast = useToastStore()
  const message = err instanceof Error ? err.message : String(err)
  toast.error('Unexpected error', message)
  console.error('[vue error]', info, err)
}

// Catch unhandled promise rejections that escape Vue's error boundary.
window.addEventListener('unhandledrejection', (event) => {
  if (isSessionExpired(event.reason)) return
  const toast = useToastStore()
  const message = event.reason instanceof Error ? event.reason.message : String(event.reason)
  toast.error('Unexpected error', message)
  console.error('[unhandledrejection]', event.reason)
})

app.mount('#app')
