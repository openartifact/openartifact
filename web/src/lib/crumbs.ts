// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { RouteLocationNormalizedLoaded } from 'vue-router'
import { t, type TranslationKey } from '../i18n/en'

/**
 * Breadcrumb declared on a route record (`meta.crumbs`). The label is either
 * an i18n key or the value of a route param (`param`); `to` may reference
 * route params as `:name` and is URL-encoded on resolution.
 */
export interface CrumbSpec {
  key?: TranslationKey
  param?: string
  to?: string
}

/** A resolved breadcrumb. The last crumb of a trail is rendered as the current page. */
export interface Crumb {
  label: string
  to?: string
}

declare module 'vue-router' {
  interface RouteMeta {
    crumbs?: CrumbSpec[]
  }
}

function paramValue(route: RouteLocationNormalizedLoaded, name: string): string {
  const raw = route.params[name]
  return Array.isArray(raw) ? raw.join('/') : String(raw ?? '')
}

/** Resolves `route.meta.crumbs` against the current params. Empty when the route declares none. */
export function resolveCrumbs(route: RouteLocationNormalizedLoaded): Crumb[] {
  const specs = route.meta.crumbs ?? []
  return specs.map((spec) => ({
    label: spec.key ? t(spec.key) : paramValue(route, spec.param ?? ''),
    to: spec.to?.replace(/:(\w+)/g, (_, p: string) => encodeURIComponent(paramValue(route, p))),
  }))
}
