// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, type ComputedRef } from 'vue'
import { useRoute, useRouter, type LocationQuery } from 'vue-router'

export interface TabDef {
  id: string
  label: string
  /** Count chip next to the label. */
  count?: number
  /** Short status text next to the label (e.g. "2 critical"). */
  flag?: string
  /** Shown but not selectable; `disabledReason` says why. */
  disabled?: boolean
  disabledReason?: string
  /** Not rendered and not reachable through the URL. */
  hidden?: boolean
}

/** The tab a raw `?tab=` value resolves to: itself when it is visible and enabled, else the first such tab. */
export function resolveTab(tabs: readonly TabDef[], raw: unknown): string {
  const usable = tabs.filter((t) => !t.hidden && !t.disabled)
  const wanted = Array.isArray(raw) ? raw[0] : raw
  return usable.find((t) => t.id === wanted)?.id ?? usable[0]?.id ?? ''
}

export interface UrlTabOptions {
  /** Query parameter name (default `tab`). */
  param?: string
  /** Other query keys that survive a tab change; everything else (filters of the old tab) is dropped. */
  keep?: string[]
}

export interface UrlTab {
  active: ComputedRef<string>
  /** Switches tab with pushState (back steps through tabs). The first tab is the default and leaves the URL clean. */
  select(id: string): Promise<void>
}

/** The active tab, bound to `?tab=`. */
export function useUrlTab(tabs: () => readonly TabDef[], opts: UrlTabOptions = {}): UrlTab {
  const param = opts.param ?? 'tab'
  const route = useRoute()
  const router = useRouter()
  const active = computed(() => resolveTab(tabs(), route.query[param]))

  async function select(id: string): Promise<void> {
    const list = tabs()
    if (id === active.value || resolveTab(list, id) !== id) return
    const query: LocationQuery = {}
    for (const key of opts.keep ?? []) if (key in route.query) query[key] = route.query[key]
    if (id !== resolveTab(list, undefined)) query[param] = id
    await router.push({ query })
  }

  return { active, select }
}
