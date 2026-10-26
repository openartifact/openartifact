// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Single source of the shell's navigation: the seven top-level areas, the
// settings sections the palette can jump to, and the role gating of both.
// The sidebar, the palette and the "recent" list all read from here, so an
// item a role cannot use is locked the same way everywhere.

import { t, type TranslationKey } from '../i18n'
import { SETTINGS_SECTIONS, type SettingsGroupId } from './settingsSections'

export type NavIcon = 'home' | 'repos' | 'packages' | 'projects' | 'bundles' | 'security' | 'settings'

export type NavAreaId = 'home' | 'repos' | 'packages' | 'projects' | 'bundles' | 'security' | 'settings'

interface NavArea {
  id: NavAreaId
  to: string
  labelKey: TranslationKey
  icon: NavIcon
  /** The area is usable by administrators only (the routes carry `requiresAdmin`). */
  adminOnly: boolean
  /** Path prefixes that keep this area highlighted (artifact pages live under /repos). */
  prefixes: readonly string[]
  /** Extra search terms for the palette; not shown, except as "Matches …". */
  keywords: string
}

export const NAV_AREAS: readonly NavArea[] = [
  { id: 'home', to: '/', labelKey: 'shell.nav.home', icon: 'home', adminOnly: false, prefixes: [], keywords: 'dashboard overview attention' },
  { id: 'repos', to: '/repos', labelKey: 'shell.nav.repos', icon: 'repos', adminOnly: false, prefixes: ['/repos'], keywords: 'repos registry' },
  { id: 'packages', to: '/packages', labelKey: 'shell.nav.packages', icon: 'packages', adminOnly: false, prefixes: ['/packages'], keywords: 'search find artifacts versions checksum' },
  { id: 'projects', to: '/projects', labelKey: 'shell.nav.projects', icon: 'projects', adminOnly: false, prefixes: ['/projects'], keywords: 'teams members' },
  { id: 'bundles', to: '/bundles', labelKey: 'shell.nav.bundles', icon: 'bundles', adminOnly: true, prefixes: ['/bundles'], keywords: 'release promote seal pipeline' },
  { id: 'security', to: '/security', labelKey: 'shell.nav.security', icon: 'security', adminOnly: false, prefixes: ['/security'], keywords: 'vulnerabilities licenses secrets findings scan' },
  { id: 'settings', to: '/settings', labelKey: 'shell.nav.settings', icon: 'settings', adminOnly: false, prefixes: ['/settings'], keywords: 'administration configuration preferences' },
]

export interface ResolvedNavItem {
  id: NavAreaId
  to: string
  label: string
  icon: NavIcon
  keywords: string
  /** The role cannot use the item: it stays visible, disabled, with `reason`. */
  locked: boolean
  reason: string | null
  /** A one-line remark on an item that stays usable (what a non-admin finds in Settings). */
  note: string | null
}

/** The areas for a role. Locked items stay in the list so the structure never changes with the role. */
export function resolveNav(isAdmin: boolean): ResolvedNavItem[] {
  return NAV_AREAS.map((a) => {
    const locked = a.adminOnly && !isAdmin
    return {
      id: a.id,
      to: a.to,
      label: t(a.labelKey),
      icon: a.icon,
      keywords: a.keywords,
      locked,
      reason: locked ? t('shell.requiresAdmin') : null,
      note: a.id === 'settings' && !isAdmin ? t('shell.settingsManagedByAdmins') : null,
    }
  })
}

/** The area a path belongs to, by route prefix; null for paths outside every area. */
export function activeAreaId(path: string): NavAreaId | null {
  if (path === '/') return 'home'
  for (const area of NAV_AREAS) {
    if (area.prefixes.some((p) => path === p || path.startsWith(`${p}/`))) return area.id
  }
  return null
}

export interface SettingsSection {
  id: string
  group: SettingsGroupId
  title: string
  groupTitle: string
  keywords: string
  /** Administrators only; the account sections and personal tokens are open to everyone. */
  adminOnly: boolean
}

/** Settings sections with their titles, for palette search; read from the settings registry. */
export function settingsSections(): SettingsSection[] {
  return SETTINGS_SECTIONS.map((s) => ({
    id: s.id,
    group: s.group,
    keywords: s.keywords.join(' '),
    adminOnly: s.adminOnly,
    title: t(s.titleKey),
    groupTitle: t(`ui.settings.group.${s.group}` as TranslationKey),
  }))
}

/** Where a settings section lives; tokens open on the personal view for people who cannot see the others. */
export function sectionPath(id: string, isAdmin: boolean): string {
  return id === 'tokens' && !isAdmin ? '/settings/tokens?type=personal' : `/settings/${id}`
}

/** The user menu's "Access tokens" entry: always the caller's own tokens. */
export const PERSONAL_TOKENS_PATH = '/settings/tokens?type=personal'
export const PROFILE_PATH = '/settings/profile'
export const PASSWORD_PATH = '/settings/password'
