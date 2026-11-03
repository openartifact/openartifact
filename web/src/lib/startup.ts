// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteComponent, Router } from 'vue-router'

/** A route component that is a loader (`() => import(...)`), as opposed to a component definition. */
function isLazyLoader(c: RouteComponent | (() => Promise<unknown>)): c is () => Promise<unknown> {
  return typeof c === 'function' && !('displayName' in c) && !('props' in c) && !('__vccOpts' in c)
}

/**
 * Starts fetching the code of the page `path` would show, so it downloads beside the session probe
 * and the language catalog instead of after them. Safe to call before the router has navigated: the
 * browser shares the module request with the navigation that follows. Never rejects; a failed chunk
 * is reported by the real navigation.
 */
export function preloadRouteChunks(router: Router, path: string): Promise<void> {
  const loaders = router
    .resolve(path)
    .matched.flatMap((record) => Object.values(record.components ?? {}))
    .filter(isLazyLoader)

  return Promise.allSettled(loaders.map((load) => load())).then(() => undefined)
}
