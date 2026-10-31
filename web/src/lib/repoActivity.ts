// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Component } from 'vue'
import {
  AdjustmentsHorizontalIcon,
  ArchiveBoxXMarkIcon,
  ArrowDownCircleIcon,
  ArrowRightCircleIcon,
  ArrowUpTrayIcon,
  ArrowUturnLeftIcon,
  ClockIcon,
  Cog6ToothIcon,
  GlobeAltIcon,
  NoSymbolIcon,
  PlusCircleIcon,
  ShieldCheckIcon,
  ShieldExclamationIcon,
  SparklesIcon,
  TrashIcon,
} from '@heroicons/vue/20/solid'
import { formatDate } from './datetime'
import { t } from '../i18n'

/** Event types of the repository timeline, as the `category` filter of the activity endpoint knows them. */
export const REPO_ACTIVITY_CATEGORIES = [
  'uploads',
  'deletions',
  'trash',
  'promotions',
  'quarantine',
  'cleanup',
  'settings',
  'federation',
] as const
export type RepoActivityCategory = (typeof REPO_ACTIVITY_CATEGORIES)[number]

/** Categories only a repository's admins may read (the server refuses them to anyone else). */
const ADMIN_ONLY: ReadonlySet<RepoActivityCategory> = new Set(['cleanup', 'settings', 'federation'])

/** The categories the caller may filter by: readers get the artifact events, admins also the operational ones. */
export function visibleCategories(admin: boolean): RepoActivityCategory[] {
  return REPO_ACTIVITY_CATEGORIES.filter((c) => admin || !ADMIN_ONLY.has(c))
}

export type ActivityTone = 'success' | 'danger' | 'warning' | 'info' | 'neutral'

export interface ActionVisual {
  icon: Component
  tone: ActivityTone
}

const VISUALS: Record<string, ActionVisual> = {
  'artifact.uploaded': { icon: ArrowUpTrayIcon, tone: 'success' },
  'artifact.deleted': { icon: TrashIcon, tone: 'danger' },
  'artifact.restored': { icon: ArrowUturnLeftIcon, tone: 'info' },
  'artifact.purged': { icon: ArchiveBoxXMarkIcon, tone: 'danger' },
  'repository.restored': { icon: ArrowUturnLeftIcon, tone: 'info' },
  'bundle.promoted': { icon: ArrowDownCircleIcon, tone: 'info' },
  'artifact.promoted': { icon: ArrowRightCircleIcon, tone: 'info' },
  'artifact.quarantined': { icon: ShieldExclamationIcon, tone: 'warning' },
  'artifact.released': { icon: ShieldCheckIcon, tone: 'success' },
  'repository.created': { icon: PlusCircleIcon, tone: 'success' },
  'repository.updated': { icon: Cog6ToothIcon, tone: 'neutral' },
  'repository.blocked': { icon: NoSymbolIcon, tone: 'warning' },
  'repository.unblocked': { icon: NoSymbolIcon, tone: 'neutral' },
  'cleanup.policy_changed': { icon: AdjustmentsHorizontalIcon, tone: 'neutral' },
  'cleanup.run_started': { icon: SparklesIcon, tone: 'neutral' },
  'cleanup.run_completed': { icon: SparklesIcon, tone: 'info' },
}

/** Icon and colour of an audit action; the federation family shares one, anything else is neutral. */
export function actionVisual(action: string): ActionVisual {
  const known = VISUALS[action]
  if (known) return known
  if (action.startsWith('federation.')) return { icon: GlobeAltIcon, tone: 'neutral' }
  return { icon: ClockIcon, tone: 'neutral' }
}

/** Design-token classes of the round badge behind an action icon. */
export function toneClasses(tone: ActivityTone): string {
  switch (tone) {
    case 'success':
      return 'bg-success-soft text-success'
    case 'danger':
      return 'bg-danger-soft text-danger'
    case 'warning':
      return 'bg-warning-soft text-warning'
    case 'info':
      return 'bg-info-soft text-info'
    default:
      return 'bg-surface-hover text-muted'
  }
}

export interface DayGroup<T> {
  /** Local calendar day, `YYYY-MM-DD`; the key of the group. */
  day: string
  /** "Today", "Yesterday" or the date. */
  label: string
  items: T[]
}

function localDay(d: Date): string {
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/**
 * Groups entries (already newest first) by local calendar day, keeping their order. Consecutive
 * entries of the same day form one group, so pages appended by infinite scroll extend the last
 * group instead of repeating its header. An entry with an unreadable timestamp joins the previous group.
 */
export function groupByDay<T extends { timestamp: string }>(entries: T[], now: Date = new Date()): DayGroup<T>[] {
  const today = localDay(now)
  const yesterday = localDay(new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1))
  const groups: DayGroup<T>[] = []

  for (const entry of entries) {
    const at = new Date(entry.timestamp)
    const last = groups[groups.length - 1]
    if (Number.isNaN(at.getTime())) {
      if (last) last.items.push(entry)
      continue
    }
    const day = localDay(at)
    if (last?.day === day) {
      last.items.push(entry)
      continue
    }
    const label = day === today ? t('rl.activity.today') : day === yesterday ? t('rl.activity.yesterday') : formatDate(at)
    groups.push({ day, label, items: [entry] })
  }

  return groups
}

/** Events whose message only restates the package line ("uploaded: lib 1.0"); the others carry detail (counts, changed settings, review kind). */
const SELF_EXPLAINING = new Set(['artifact.uploaded', 'artifact.deleted', 'artifact.restored', 'artifact.purged'])

/** The line under an entry: its message, unless the entry's package subject already says it. */
export function entryDetail(e: { action: string; message?: string; subject: { kind: string } | null }): string {
  if (!e.message) return ''
  return SELF_EXPLAINING.has(e.action) && e.subject?.kind === 'package' ? '' : e.message
}
