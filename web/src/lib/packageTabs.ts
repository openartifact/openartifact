// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { LocationQuery, LocationQueryRaw } from 'vue-router'

/** Tabs of the package detail page; the first is the default. */
export const PACKAGE_TABS = ['overview', 'versions', 'properties', 'security'] as const

export type PackageTab = (typeof PACKAGE_TABS)[number]

/** The tab a `?tab=` query value names; unknown or missing values open the default one. */
export function resolvePackageTab(value: unknown): PackageTab {
  const wanted = Array.isArray(value) ? value[0] : value
  return PACKAGE_TABS.find((tab) => tab === wanted) ?? PACKAGE_TABS[0]
}

/**
 * The route query that opens `tab`: the other query keys are kept and the
 * default tab leaves no `tab` key, so the plain package URL stays canonical.
 */
export function packageTabQuery(query: LocationQuery, tab: PackageTab): LocationQueryRaw {
  const { tab: _drop, ...rest } = query
  return tab === PACKAGE_TABS[0] ? rest : { ...rest, tab }
}
