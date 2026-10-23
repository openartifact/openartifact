// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Service worker of the installable app. Deliberately small:
//
//  - It precaches the hashed build assets and the static app shell (offline
//    page, icons, manifest). It caches nothing else: no API, repository,
//    registry or authenticated response ever passes through a cache (see
//    swRoutes.ts for the routes it leaves alone).
//  - Page navigations always go to the network, so the UI is never an old
//    index.html; only when the network is unreachable the offline page is
//    shown. No application data is served offline.
//  - An updated worker installs and then waits. The page offers a reload
//    (register.ts) and only that message activates it, so a form is never
//    interrupted by an update.
//
// Built by the `pwa` plugin in vite.config.ts, which injects __PRECACHE_VERSION__ and __PRECACHE_URLS__.

import { classifyRequest } from './swRoutes'

declare const __PRECACHE_VERSION__: string
declare const __PRECACHE_URLS__: string[]

// This file is compiled against the WebWorker lib (tsconfig.sw.json).
const sw = self as unknown as ServiceWorkerGlobalScope

const CACHE_PREFIX = 'oa-precache-'
const CACHE_NAME = CACHE_PREFIX + __PRECACHE_VERSION__
const OFFLINE_URL = '/offline.html'
const precached: ReadonlySet<string> = new Set(__PRECACHE_URLS__)

/** Content-hashed build output: the same URL always has the same bytes. */
function isHashedAsset(url: string): boolean {
  return url.startsWith('/assets/')
}

async function precache(): Promise<void> {
  const cache = await caches.open(CACHE_NAME)
  const previous = (await caches.keys()).filter((k) => k.startsWith(CACHE_PREFIX) && k !== CACHE_NAME)

  await Promise.all(
    __PRECACHE_URLS__.map(async (url) => {
      if (await cache.match(url)) return

      if (isHashedAsset(url)) {
        // Unchanged assets keep their hashed name across releases: reuse them.
        for (const name of previous) {
          const hit = await (await caches.open(name)).match(url)
          if (hit) {
            await cache.put(url, hit)
            return
          }
        }
      }

      const res = await fetch(url, { cache: 'reload', credentials: 'omit' })
      if (!res.ok || res.redirected) throw new Error(`precache ${url}: ${res.status}`)
      await cache.put(url, res)
    }),
  )
}

async function dropOldCaches(): Promise<void> {
  const keys = await caches.keys()
  await Promise.all(keys.filter((k) => k.startsWith(CACHE_PREFIX) && k !== CACHE_NAME).map((k) => caches.delete(k)))
}

async function offlinePage(): Promise<Response> {
  const hit = await caches.match(OFFLINE_URL, { cacheName: CACHE_NAME })
  return hit ?? new Response('You are offline', { status: 503, headers: { 'Content-Type': 'text/plain' } })
}

sw.addEventListener('install', (event) => {
  // No skipWaiting() here: activation is requested by the page (see below).
  event.waitUntil(precache())
})

sw.addEventListener('activate', (event) => {
  event.waitUntil(dropOldCaches().then(() => sw.clients.claim()))
})

sw.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'SKIP_WAITING') void sw.skipWaiting()
})

sw.addEventListener('fetch', (event) => {
  const { request } = event
  const kind = classifyRequest(
    { method: request.method, url: request.url, mode: request.mode, accept: request.headers.get('Accept') },
    sw.location.origin,
    precached,
  )

  if (kind === 'precache') {
    event.respondWith(
      caches.match(request, { cacheName: CACHE_NAME }).then((hit) => hit ?? fetch(request)),
    )
  } else if (kind === 'navigate') {
    event.respondWith(fetch(request).catch(() => offlinePage()))
  }
})
