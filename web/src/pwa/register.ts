// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useToastStore } from '../stores/toast'
import { t } from '../i18n/en'

const SW_URL = '/sw.js'
const UPDATE_CHECK_MS = 60 * 60 * 1000

export interface ServiceWorkerOptions {
  container?: ServiceWorkerContainer
  reload?: () => void
  /** Notified once per waiting worker; `apply` activates it and reloads the page. */
  onUpdate?: (apply: () => void) => void
}

/**
 * Registers the service worker and reports waiting updates. The new worker is
 * activated, and the page reloaded, only when `apply` is called: an update
 * never replaces the page the user is working on by itself.
 */
export function registerServiceWorker(opts: ServiceWorkerOptions = {}): Promise<ServiceWorkerRegistration | undefined> {
  const container = opts.container ?? (typeof navigator === 'undefined' ? undefined : navigator.serviceWorker)
  if (!container) return Promise.resolve(undefined)

  const reload = opts.reload ?? (() => window.location.reload())
  const announced = new WeakSet<ServiceWorker>()
  let registration: ServiceWorkerRegistration | undefined
  let applying = false

  container.addEventListener('controllerchange', () => {
    // Only the reload the user asked for; the first install also changes the controller.
    if (applying) reload()
  })

  const announce = (worker: ServiceWorker | null) => {
    // A waiting worker next to no controller is the first install, not an update.
    if (!worker || !container.controller || announced.has(worker)) return
    announced.add(worker)
    opts.onUpdate?.(() => {
      applying = true
      // A newer release may have replaced the worker this offer was made for.
      const target = registration?.waiting ?? worker
      target.postMessage({ type: 'SKIP_WAITING' })
    })
  }

  return container
    .register(SW_URL, { scope: '/' })
    .then((reg) => {
      registration = reg
      announce(reg.waiting)
      reg.addEventListener('updatefound', () => {
        const installing = reg.installing
        installing?.addEventListener('statechange', () => {
          if (installing.state === 'installed') announce(installing)
        })
      })
      // Long-lived tabs and installed apps rarely navigate: poll for a new worker.
      setInterval(() => void reg.update().catch(() => undefined), UPDATE_CHECK_MS)
      return reg
    })
    .catch((err) => {
      console.warn('[pwa] service worker registration failed', err)
      return undefined
    })
}

/** Registers the worker after page load and offers a non-blocking reload toast on updates. */
export function startServiceWorker(): void {
  let offer: number | undefined

  const register = () =>
    void registerServiceWorker({
      onUpdate: (apply) => {
        const toasts = useToastStore()
        // One standing offer: a newer release supersedes the previous one.
        if (offer !== undefined) toasts.dismiss(offer)
        offer = toasts.push({
          level: 'info',
          title: t('pwa.updateAvailable'),
          description: t('pwa.updateHint'),
          action: { label: t('pwa.reload'), run: apply },
          timeoutMs: 0,
        })
      },
    })

  if (document.readyState === 'complete') register()
  else window.addEventListener('load', register, { once: true })
}
