// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The palette's "Recent" list: route-level items only (area pages, settings
// sections and the entities the user opened). It is derived from the visited
// path, so opening something through the palette and through a link record the
// same thing, and it holds no query text, no secrets and no API data.

import { NAV_AREAS, settingsSections } from './navigation'
import { t } from '../i18n/en'

export type RecentKind = 'page' | 'section' | 'repo' | 'package' | 'project' | 'bundle'

export interface RecentItem {
  kind: RecentKind
  /** In-app path, always starting with a single "/". */
  to: string
  label: string
  sub?: string
}

export const RECENT_LIMIT = 8

const KINDS: readonly RecentKind[] = ['page', 'section', 'repo', 'package', 'project', 'bundle']

/** The key of one user's list, so a shared browser never shows someone else's history. */
export function recentStorageKey(userId: string | undefined): string {
  return `oa.palette.recent.${userId || 'anonymous'}`
}

/** Puts `item` first, drops the earlier entry for the same path and keeps the list bounded. */
export function pushRecent(list: readonly RecentItem[], item: RecentItem, limit = RECENT_LIMIT): RecentItem[] {
  return [item, ...list.filter((r) => r.to !== item.to)].slice(0, limit)
}

function isRecentItem(v: unknown): v is RecentItem {
  if (typeof v !== 'object' || v === null) return false
  const r = v as Record<string, unknown>
  return (
    typeof r.to === 'string' &&
    r.to.startsWith('/') &&
    !r.to.startsWith('//') &&
    typeof r.label === 'string' &&
    KINDS.includes(r.kind as RecentKind) &&
    (r.sub === undefined || typeof r.sub === 'string')
  )
}

/** Reads the stored list; anything malformed (or storage being unavailable) yields an empty list. */
export function loadRecent(storage: Pick<Storage, 'getItem'> | undefined, key: string): RecentItem[] {
  try {
    const parsed: unknown = JSON.parse(storage?.getItem(key) ?? '[]')
    return Array.isArray(parsed) ? parsed.filter(isRecentItem).slice(0, RECENT_LIMIT) : []
  } catch {
    return []
  }
}

export function saveRecent(storage: Pick<Storage, 'setItem'> | undefined, key: string, list: readonly RecentItem[]): void {
  try {
    storage?.setItem(key, JSON.stringify(list))
  } catch {
    // storage full or blocked: recent items are a convenience
  }
}

function decode(segment: string): string {
  try {
    return decodeURIComponent(segment)
  } catch {
    return segment
  }
}

/** What a visited path counts as in the Recent list, or null when it is not worth remembering. */
export function recentFromPath(path: string): RecentItem | null {
  const area = NAV_AREAS.find((a) => a.to === path && a.id !== 'home')
  if (area) return { kind: 'page', to: path, label: t(area.labelKey) }

  const section = /^\/settings\/([^/]+)$/.exec(path)
  if (section) {
    const found = settingsSections().find((s) => s.id === decode(section[1]!))
    return found ? { kind: 'section', to: path, label: found.title, sub: found.groupTitle } : null
  }

  const pkg = /^\/repos\/([^/]+)\/packages\/(.+?)(?:\/versions\/(.+))?$/.exec(path)
  if (pkg) {
    const version = pkg[3] ? ` ${decode(pkg[3])}` : ''
    return { kind: 'package', to: path, label: `${decode(pkg[2]!)}${version}`, sub: decode(pkg[1]!) }
  }

  const repo = /^\/repos\/([^/]+)$/.exec(path)
  if (repo) return { kind: 'repo', to: path, label: decode(repo[1]!) }

  const project = /^\/projects\/([^/]+)$/.exec(path)
  if (project) return { kind: 'project', to: path, label: decode(project[1]!) }

  const bundle = /^\/bundles\/([^/]+)\/([^/]+)$/.exec(path)
  if (bundle) return { kind: 'bundle', to: path, label: `${decode(bundle[1]!)} ${decode(bundle[2]!)}` }

  return null
}
