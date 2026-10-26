// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n'
import { SETTINGS_GROUPS, SETTINGS_SECTIONS, type SettingsGroupId, type SettingsSection } from './settingsSections'

/** Fields a query can hit, best first (the order of `rank`). */
export type SettingsMatchField = 'title' | 'keyword' | 'description' | 'id' | 'words'

export interface SettingsMatch {
  section: SettingsSection
  /** 0 title, 1 keyword, 2 description (or a multi-word query spread over fields), 3 section id. */
  rank: number
  fields: SettingsMatchField[]
  /** The keywords that matched (at most two, for the "Matches … in keywords (…)" line). */
  keywords: string[]
  /** Substring (start, end) of the query inside the title, for highlighting; null when the title did not match. */
  titleRange: [number, number] | null
  descRange: [number, number] | null
  /** A substring match was used because no word-prefix match exists anywhere. */
  loose: boolean
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** First hit of `q` in `text`: a word-prefix match, or any substring in loose mode. Case-insensitive. */
export function findRange(text: string, q: string, loose: boolean): [number, number] | null {
  if (!q) return null
  if (loose) {
    const i = text.toLowerCase().indexOf(q.toLowerCase())
    return i < 0 ? null : [i, i + q.length]
  }
  const m = new RegExp(`(^|[^a-z0-9])(${escapeRegExp(q)})`, 'i').exec(text)
  if (!m) return null
  const start = m.index + m[1].length
  return [start, start + q.length]
}

function matchOne(section: SettingsSection, q: string, loose: boolean): SettingsMatch | null {
  const title = t(section.titleKey)
  const desc = t(section.descKey)
  const titleRange = findRange(title, q, loose)
  const descRange = findRange(desc, q, loose)
  const keywords = section.keywords.filter((k) => findRange(k, q, loose) !== null)
  const idHit = !titleRange && findRange(section.id, q, loose) !== null

  const fields: SettingsMatchField[] = []
  if (titleRange) fields.push('title')
  if (keywords.length) fields.push('keyword')
  if (descRange) fields.push('description')
  if (idHit) fields.push('id')

  let rank = titleRange ? 0 : keywords.length ? 1 : descRange ? 2 : idHit ? 3 : -1
  // A phrase that is not in any single field still matches when every word is somewhere.
  if (rank < 0 && /\s/.test(q)) {
    const words = q.split(/\s+/).filter(Boolean)
    const haystack = [title, desc, ...section.keywords]
    if (words.every((w) => haystack.some((h) => findRange(h, w, loose) !== null))) {
      rank = 2
      fields.push('words')
    }
  }
  if (rank < 0) return null
  return { section, rank, fields, keywords: keywords.slice(0, 2), titleRange, descRange, loose }
}

/**
 * Searches title, keywords, description and id of the given sections. Word-prefix
 * matches come first; only when there is none anywhere does it fall back to
 * substrings. Ordered by rank (title first), then by registry order.
 */
export function searchSettings(query: string, pool: readonly SettingsSection[] = SETTINGS_SECTIONS): SettingsMatch[] {
  const q = query.trim()
  if (!q) return []
  for (const loose of [false, true]) {
    const hits = pool.map((s) => matchOne(s, q, loose)).filter((m): m is SettingsMatch => m !== null)
    if (hits.length) {
      const order = new Map(pool.map((s, i) => [s.id, i]))
      return hits.sort((a, b) => a.rank - b.rank || order.get(a.section.id)! - order.get(b.section.id)!)
    }
  }
  return []
}

/** One hub group: `available` sections open for the role, `locked` ones only for administrators. */
export interface HubGroup {
  id: SettingsGroupId
  titleKey: (typeof SETTINGS_GROUPS)[number]['titleKey']
  descKey: (typeof SETTINGS_GROUPS)[number]['descKey']
  available: SettingsSection[]
  locked: SettingsSection[]
}

/**
 * The hub layout for a role. Administrators get every section available. Everybody else gets the sections
 * they may open (profile, password, personal tokens) collected under "Your account" and everything else as locked,
 * so the hub's structure never changes with the role.
 */
export function hubGroups(isAdmin: boolean): HubGroup[] {
  if (isAdmin) {
    return SETTINGS_GROUPS.map((g) => ({
      ...g,
      available: SETTINGS_SECTIONS.filter((s) => s.group === g.id),
      locked: [],
    })).filter((g) => g.available.length > 0)
  }
  const open = SETTINGS_SECTIONS.filter((s) => !s.adminOnly)
  return SETTINGS_GROUPS.map((g) => {
    const isAccount = g.id === 'account'
    return {
      ...g,
      available: isAccount ? open : [],
      locked: SETTINGS_SECTIONS.filter((s) => s.group === g.id && s.adminOnly),
    }
  }).filter((g) => g.available.length > 0 || g.locked.length > 0)
}

/** Sections the role can open, in hub order. */
export function openableSections(isAdmin: boolean): SettingsSection[] {
  return SETTINGS_SECTIONS.filter((s) => isAdmin || !s.adminOnly)
}

/** Sections that match `query` but are closed to the role (the hub says how many). */
export function lockedMatchCount(query: string, isAdmin: boolean): number {
  if (isAdmin) return 0
  return searchSettings(query, SETTINGS_SECTIONS.filter((s) => s.adminOnly)).filter((m) => !m.loose).length
}

/** The group a section is listed under for a role: non-administrators find personal tokens under "Your account". */
export function groupOf(section: SettingsSection, isAdmin: boolean): SettingsGroupId {
  return !isAdmin && !section.adminOnly ? 'account' : section.group
}

export interface RailGroup {
  id: SettingsGroupId
  titleKey: HubGroup['titleKey']
  sections: SettingsSection[]
  /** The group of the open section: expanded, listed first. */
  current: boolean
}

/**
 * The sibling rail of a section page: the open section's group first, then the other groups
 * (rendered collapsed). Only sections the role can open are listed.
 */
export function siblingRail(currentId: string, isAdmin: boolean): RailGroup[] {
  const open = openableSections(isAdmin)
  const found = SETTINGS_SECTIONS.find((s) => s.id === currentId)
  const current = found ? groupOf(found, isAdmin) : undefined
  const groups = SETTINGS_GROUPS.map((g) => ({
    id: g.id,
    titleKey: g.titleKey,
    sections: open.filter((s) => groupOf(s, isAdmin) === g.id),
    current: g.id === current,
  })).filter((g) => g.sections.length > 0)
  return [...groups.filter((g) => g.current), ...groups.filter((g) => !g.current)]
}
