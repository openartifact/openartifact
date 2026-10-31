// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

const SW_URL = '/sw.js'
const UPDATE_CHECK_MS = 60 * 60 * 1000

interface ServiceWorkerOptions {
  container?: ServiceWorkerContainer
  reload?: () => void
  /** Notified once per waiting worker; `apply` activates it and reloads the page. */
  onUpdate?: (apply: () => void) => void
  /**
   * Path of a content-hashed asset of the running page (its entry script).
   * A waiting worker that precaches it belongs to the release already on
   * screen, so no reload is offered. Defaults to the document's module script.
   */
  pageAsset?: string | null
  /** A worker of the release on screen took over quietly: any standing offer is moot. */
  onCurrent?: () => void
}

/** How long a waiting worker may take to say what it precaches. */
const ASSET_QUERY_MS = 3000

/** Path of the running page's entry script, null when there is none. */
function entryScript(): string | null {
  if (typeof document === 'undefined') return null
  const src = document.querySelector<HTMLScriptElement>('script[type="module"][src]')?.src
  return src ? new URL(src).pathname : null
}

/**
 * Asks worker whether its precache lists url (sw.ts answers HAS_ASSET on the
 * given port); null when it does not answer in time.
 */
function precaches(worker: ServiceWorker, url: string): Promise<boolean | null> {
  // Executor form: the app's TypeScript lib (and older browsers) predate Promise.withResolvers.
  return new Promise((resolve) => {
    const channel = new MessageChannel()
    const timer = setTimeout(() => resolve(null), ASSET_QUERY_MS)
    channel.port1.onmessage = (e: MessageEvent) => {
      clearTimeout(timer)
      channel.port1.close()
      resolve(e.data === true)
    }
    worker.postMessage({ type: 'HAS_ASSET', url }, [channel.port2])
  })
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
  const pageAsset = opts.pageAsset === undefined ? entryScript() : opts.pageAsset
  const announced = new WeakSet<ServiceWorker>()
  let registration: ServiceWorkerRegistration | undefined
  let applying = false

  container.addEventListener('controllerchange', () => {
    // Only the reload the user asked for; the first install also changes the controller.
    if (applying) reload()
  })

  const announce = async (worker: ServiceWorker | null) => {
    // A waiting worker next to no controller is the first install, not an update.
    if (!worker || !container.controller || announced.has(worker)) return
    announced.add(worker)

    // Navigations always reach the network, so a page loaded after the deploy
    // already runs the release the waiting worker belongs to: activate it
    // quietly (no reload, nothing changes for the user) instead of offering one.
    if (pageAsset && (await precaches(worker, pageAsset)) === true) {
      worker.postMessage({ type: 'SKIP_WAITING' })
      // An offer made for an older worker that was still waiting at load is withdrawn.
      opts.onCurrent?.()
      return
    }

    opts.onUpdate?.(() => {
      // A newer release may have replaced the worker this offer was made for.
      const target = registration?.waiting ?? worker
      // Another tab may have activated it already: then only the reload is left.
      if (target.state === 'activating' || target.state === 'activated') {
        reload()
        return
      }
      applying = true
      target.postMessage({ type: 'SKIP_WAITING' })
    })
  }

  return container
    .register(SW_URL, { scope: '/' })
    .then((reg) => {
      registration = reg
      void announce(reg.waiting)
      reg.addEventListener('updatefound', () => {
        const installing = reg.installing
        installing?.addEventListener('statechange', () => {
          if (installing.state === 'installed') void announce(installing)
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
      onCurrent: () => {
        if (offer !== undefined) useToastStore().dismiss(offer)
        offer = undefined
      },
    })

  if (document.readyState === 'complete') register()
  else window.addEventListener('load', register, { once: true })
}

/** What a server reports as its version when it was built without a release stamp (a plain `go build`). */
const DEV_VERSION = 'dev'

/** Unregisters every service worker of this origin, so a development instance stops serving a stale shell. */
export async function retireServiceWorkers(container: ServiceWorkerContainer | undefined = navigatorContainer()): Promise<void> {
  if (!container?.getRegistrations) return
  const registrations = await container.getRegistrations().catch(() => [])
  await Promise.all(registrations.map((r) => r.unregister().catch(() => false)))
}

function navigatorContainer(): ServiceWorkerContainer | undefined {
  return typeof navigator === 'undefined' ? undefined : navigator.serviceWorker
}

/**
 * The service worker of a production page. A server built without a release stamp (version
 * "dev") is a development instance: every rebuild changes the precache, so a worker there would
 * offer an update after each one. It gets no worker, and one an earlier visit installed is
 * removed. Anything else, including a server that does not say its version, is production.
 */
export async function startServiceWorkerForServer(serverVersion: () => Promise<string | null>): Promise<void> {
  if ((await serverVersion()) === DEV_VERSION) {
    await retireServiceWorkers()
    return
  }
  startServiceWorker()
}
