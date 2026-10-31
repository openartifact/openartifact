// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// The palette's "Recent" list: route-level items (area pages, settings
// sections and the entities the user opened), derived from the visited path so
// opening something through the palette and through a link record the same
// thing, and the actions the user ran from the palette (an action id and, for
// a repository action, the repository; never a label, which follows the
// language). It holds no query text, no secrets and no API data.

import { NAV_AREAS, settingsSections } from './navigation'
import { actionRecentKey, isRepoSubject, type ActionRecent } from './paletteActions'
import { t } from '../i18n'

export type RecentKind = 'page' | 'section' | 'repo' | 'package' | 'project' | 'bundle'

export interface RouteRecent {
  kind: RecentKind
  /** In-app path, always starting with a single "/". */
  to: string
  label: string
  sub?: string
}

export type RecentItem = RouteRecent | ActionRecent

export const RECENT_LIMIT = 8

const KINDS: readonly RecentKind[] = ['page', 'section', 'repo', 'package', 'project', 'bundle']

/** The key of one user's list, so a shared browser never shows someone else's history. */
export function recentStorageKey(userId: string | undefined): string {
  return `oa.palette.recent.${userId || 'anonymous'}`
}

/** What makes two entries the same: the path of a route, the action and repository of an action run. */
export function recentKey(r: RecentItem): string {
  return r.kind === 'action' ? actionRecentKey(r) : r.to
}

/** Puts `item` first, drops the earlier entry for the same thing and keeps the list bounded. */
export function pushRecent(list: readonly RecentItem[], item: RecentItem, limit = RECENT_LIMIT): RecentItem[] {
  const key = recentKey(item)
  return [item, ...list.filter((r) => recentKey(r) !== key)].slice(0, limit)
}

function isRecentItem(v: unknown): v is RecentItem {
  if (typeof v !== 'object' || v === null) return false
  const r = v as Record<string, unknown>
  if (r.kind === 'action') return typeof r.action === 'string' && (r.repo === undefined || isRepoSubject(r.repo))
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
export function recentFromPath(path: string): RouteRecent | null {
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
