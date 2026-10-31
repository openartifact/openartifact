// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure logic of the command palette: query parsing, matching and ranking,
// grouping, keyboard movement. The component and the search composable only
// feed it data, so all of it is tested without a DOM.

import { resolveNav, sectionPath, settingsSections, type NavIcon } from './navigation'
import {
  bindRecentAction,
  globalActions,
  languageActions,
  repoActions,
  repoSearchTerm,
  type BoundAction,
  type PaletteContext,
  type RepoSubject,
} from './paletteActions'
import { recentKey, type RecentItem } from './paletteRecent'
import { t, tp } from '../i18n'

export type PaletteGroupId =
  | 'recent'
  | 'pages'
  | 'repositories'
  | 'packages'
  | 'projects'
  | 'bundles'
  | 'settings'
  | 'users'
  | 'actions'
  | 'search'

export type PaletteIcon =
  | NavIcon
  | 'plus'
  | 'ticket'
  | 'user-plus'
  | 'tag'
  | 'theme'
  | 'sign-out'
  | 'clipboard'
  | 'language'
  | 'document'
  | 'user'
  | 'search'

export interface PaletteItem {
  id: string
  group: PaletteGroupId
  title: string
  description?: string
  /** Extra search terms; never shown except as the "Matches" hint. */
  keywords?: string
  icon: PaletteIcon
  /** Package format of the item, rendered as a format badge instead of the icon. */
  format?: string
  /** Set in monospace (names and identifiers). */
  mono?: boolean
  /** In-app destination (may carry a query string). */
  to?: string
  /** Something to run instead of navigating. */
  action?: BoundAction
  /** The repository a repository result stands for; its actions are offered when it matches. */
  repo?: RepoSubject
  /** The role cannot use the item: it is listed, explains why, and does nothing. */
  locked?: boolean
}

export interface PaletteGroup {
  id: PaletteGroupId
  items: PaletteItem[]
}

export interface ParsedQuery {
  /** `actions`: the `>` prefix, which narrows the list to actions only. */
  mode: 'all' | 'actions'
  /** The text to match, without the prefix, trimmed. */
  text: string
  /** Lower-cased hex digest when the text looks like a checksum, else null. */
  checksum: string | null
}

const CHECKSUM_RE = /^[0-9a-f]{32,64}$/i

export function parseQuery(raw: string): ParsedQuery {
  const trimmed = raw.trim()
  const mode = trimmed.startsWith('>') ? 'actions' : 'all'
  const text = (mode === 'actions' ? trimmed.slice(1) : trimmed).trim()
  return { mode, text, checksum: mode === 'all' && CHECKSUM_RE.test(text) ? text.toLowerCase() : null }
}

/** Where a checksum lookup goes: the Packages page in checksum mode. */
export function checksumPath(hex: string): string {
  return `/packages?q=${encodeURIComponent(hex)}&mode=checksum`
}

/** Where "search all packages" goes. */
export function packageSearchPath(text: string): string {
  return `/packages?q=${encodeURIComponent(text)}`
}

function wordStart(haystack: string, needle: string): boolean {
  return haystack.split(/[\s\-_/.:@]+/).some((w) => w.startsWith(needle))
}

function tokenScore(item: Pick<PaletteItem, 'title' | 'keywords' | 'description'>, token: string): number {
  const title = item.title.toLowerCase()
  if (title === token) return 100
  if (title.startsWith(token)) return 80
  if (wordStart(title, token)) return 60
  if (title.includes(token)) return 40
  const kw = (item.keywords ?? '').toLowerCase()
  if (wordStart(kw, token)) return 30
  if (kw.includes(token)) return 20
  if ((item.description ?? '').toLowerCase().includes(token)) return 10
  return 0
}

/**
 * How well `item` matches `text`: 0 for no match. Every whitespace-separated
 * token must match somewhere (title beats keywords beats description); an empty
 * text matches everything equally.
 */
export function matchScore(item: Pick<PaletteItem, 'title' | 'keywords' | 'description'>, text: string): number {
  const tokens = text.toLowerCase().split(/\s+/).filter(Boolean)
  if (tokens.length === 0) return 1
  let total = 0
  for (const token of tokens) {
    const s = tokenScore(item, token)
    if (s === 0) return 0
    total += s
  }
  return total
}

/** Items that match `text`, best first; equal scores keep their given order. */
export function rankItems<T extends PaletteItem>(items: readonly T[], text: string): T[] {
  return items
    .map((item, index) => ({ item, index, score: matchScore(item, text) }))
    .filter((r) => r.score > 0)
    .sort((a, b) => b.score - a.score || a.index - b.index)
    .map((r) => r.item)
}

/** `text` split around the first case-insensitive occurrence of `query`, for highlighting. */
export function highlightParts(text: string, query: string): { text: string; match: boolean }[] {
  const q = query.trim().toLowerCase()
  const i = q ? text.toLowerCase().indexOf(q) : -1
  if (i < 0) return [{ text, match: false }]
  return [
    { text: text.slice(0, i), match: false },
    { text: text.slice(i, i + q.length), match: true },
    { text: text.slice(i + q.length), match: false },
  ].filter((p) => p.text !== '')
}

// ---------------------------------------------------------------------------
// Static items

export function pageItems(ctx: PaletteContext): PaletteItem[] {
  return resolveNav(ctx.isAdmin).map((n) => ({
    id: `page:${n.id}`,
    group: 'pages',
    title: n.label,
    keywords: n.keywords,
    icon: n.icon,
    to: n.to,
    locked: n.locked,
  }))
}

export function sectionItems(ctx: PaletteContext): PaletteItem[] {
  return settingsSections().map((s) => ({
    id: `section:${s.id}`,
    group: 'settings',
    title: s.title,
    description: s.groupTitle,
    keywords: s.keywords,
    icon: 'settings',
    to: sectionPath(s.id, ctx.isAdmin),
    locked: s.adminOnly && !ctx.isAdmin,
  }))
}

/** A bound action as a list item: in the Actions group, or in Recent. */
export function actionItem(action: BoundAction, group: 'actions' | 'recent' = 'actions'): PaletteItem {
  return {
    id: `${group === 'recent' ? 'recent:action' : 'action'}:${action.key}`,
    group,
    title: action.title,
    description: action.description,
    keywords: action.keywords,
    icon: action.icon,
    action,
  }
}

/** How many of the best-matching repositories get their own actions. */
const REPO_ACTION_REPOS = 3

/**
 * Every action the caller may run for `text`, unranked. Without text only the
 * global ones are offered; with text the language choices and the actions of
 * the best-matching repositories join them (they are too many to list blind).
 */
export function actionItems(ctx: PaletteContext, text = '', repositories: readonly PaletteItem[] = []): PaletteItem[] {
  const actions = globalActions(ctx)
  if (text) {
    actions.push(...languageActions(ctx))
    const repos = rankItems(repositories, repoSearchTerm(text, ctx)).flatMap((r) => (r.repo ? [r.repo] : []))
    for (const repo of repos.slice(0, REPO_ACTION_REPOS)) actions.push(...repoActions(ctx, repo))
  }
  return actions.map((a) => actionItem(a))
}

const RECENT_ICON: Record<Exclude<RecentItem['kind'], 'action'>, PaletteIcon> = {
  page: 'document',
  section: 'settings',
  repo: 'repos',
  package: 'packages',
  project: 'projects',
  bundle: 'bundles',
}

/** Recent entries as items, skipping those the role can no longer open or run. */
export function recentItems(recent: readonly RecentItem[], ctx: PaletteContext): PaletteItem[] {
  const lockedPaths = new Set<string>()
  for (const p of pageItems(ctx)) if (p.locked && p.to) lockedPaths.add(p.to)
  for (const s of sectionItems(ctx)) if (s.locked && s.to) lockedPaths.add(s.to)
  return recent.flatMap((r): PaletteItem[] => {
    if (r.kind === 'action') {
      const action = bindRecentAction(r, ctx)
      return action ? [actionItem(action, 'recent')] : []
    }
    if (lockedPaths.has(r.to) || (r.kind === 'bundle' && !ctx.isAdmin)) return []
    return [
      {
        id: `recent:${recentKey(r)}`,
        group: 'recent',
        title: r.label,
        description: r.sub,
        icon: RECENT_ICON[r.kind],
        mono: r.kind !== 'page' && r.kind !== 'section',
        to: r.to,
      },
    ]
  })
}

// ---------------------------------------------------------------------------
// Grouping

export interface EntityResults {
  repositories: PaletteItem[]
  packages: PaletteItem[]
  projects: PaletteItem[]
  bundles: PaletteItem[]
  users: PaletteItem[]
}

export const EMPTY_ENTITIES: EntityResults = { repositories: [], packages: [], projects: [], bundles: [], users: [] }

export interface PaletteSources {
  query: ParsedQuery
  context: PaletteContext
  recent: readonly RecentItem[]
  entities: EntityResults
}

const LIMITS: Record<PaletteGroupId, number> = {
  recent: 5,
  pages: 7,
  repositories: 5,
  packages: 5,
  projects: 4,
  bundles: 4,
  settings: 5,
  users: 4,
  actions: 10,
  search: 2,
}

/** The "search" group: the checksum lookup (first, when the text is a digest) or "search all packages". */
function searchItems(query: ParsedQuery): PaletteItem[] {
  if (query.checksum) {
    return [
      {
        id: 'search:checksum',
        group: 'search',
        title: t('shell.palette.checksum'),
        description: query.checksum,
        icon: 'packages',
        mono: false,
        to: checksumPath(query.checksum),
      },
    ]
  }
  return [
    {
      id: 'search:packages',
      group: 'search',
      title: tp('shell.palette.searchPackages', { q: query.text }),
      icon: 'search',
      to: packageSearchPath(query.text),
    },
  ]
}

/** Tells why a settings section matched when only its keywords did, e.g. "Matches “retention” · Storage". */
function withKeywordHint(items: PaletteItem[], text: string): PaletteItem[] {
  const q = text.toLowerCase()
  return items.map((item) => {
    if (item.title.toLowerCase().includes(q)) return item
    const kw = (item.keywords ?? '').split(/\s+/).find((k) => k.includes(q))
    return kw ? { ...item, description: tp('shell.palette.matches', { kw, group: item.description ?? '' }) } : item
  })
}

/**
 * The groups to show, in display order.
 * - Empty text: Recent, Pages, a few suggested Actions.
 * - `>` prefix: Actions only.
 * - Otherwise: ranked static matches (Pages, Settings, Actions, which include
 *   the actions of the matching repositories), the entity
 *   results as they arrive (already filtered by the server for packages, ranked
 *   here for the rest), and the search row, which leads when the text is a
 *   checksum.
 */
export function buildGroups({ query, context, recent, entities }: PaletteSources): PaletteGroup[] {
  const out: PaletteGroup[] = []
  const add = (id: PaletteGroupId, items: PaletteItem[]) => {
    if (items.length > 0) out.push({ id, items: items.slice(0, LIMITS[id]) })
  }

  if (query.mode === 'actions') {
    add('actions', rankItems(actionItems(context, query.text, entities.repositories), query.text))
    return out
  }

  if (!query.text) {
    add('recent', recentItems(recent, context))
    add('pages', pageItems(context))
    add('actions', actionItems(context).slice(0, 4))
    return out
  }

  if (query.checksum) add('search', searchItems(query))
  add('pages', rankItems(pageItems(context), query.text))
  add('repositories', rankItems(entities.repositories, repoSearchTerm(query.text, context)))
  add('packages', entities.packages)
  add('projects', rankItems(entities.projects, query.text))
  add('bundles', rankItems(entities.bundles, query.text))
  add('settings', withKeywordHint(rankItems(sectionItems(context), query.text), query.text))
  add('users', rankItems(entities.users, query.text))
  add('actions', rankItems(actionItems(context, query.text, entities.repositories), query.text))
  if (!query.checksum) add('search', searchItems(query))
  return out
}

export function flattenGroups(groups: readonly PaletteGroup[]): PaletteItem[] {
  return groups.flatMap((g) => g.items)
}

/** The hint shown on the right of an item: why it is locked, or that it runs. */
export function itemHint(item: PaletteItem): string | null {
  if (item.locked) return t('shell.palette.requiresAdmin')
  if (item.action) return t('shell.palette.actionHint')
  return null
}

// ---------------------------------------------------------------------------
// Keyboard

/** The active index after a navigation key; `null` when the key is not one. */
export function moveActive(key: string, current: number, count: number): number | null {
  if (count <= 0) return null
  switch (key) {
    case 'ArrowDown':
      return (current + 1) % count
    case 'ArrowUp':
      return (current - 1 + count) % count
    case 'Home':
      return 0
    case 'End':
      return count - 1
    default:
      return null
  }
}
