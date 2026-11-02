// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { DeclarativeRun, DeclarativeRunItem } from '../api/declarative'

export type PlanAction = DeclarativeRunItem['action']

/** Order of the actions in a summary and in the change list: what a reader must check first comes first. */
export const PLAN_ACTIONS: readonly PlanAction[] = ['delete', 'create', 'update', 'unchanged']

/** The count of every action in the plan, in {@link PLAN_ACTIONS} order, without the actions that have none. */
export function summaryCounts(run: DeclarativeRun): { action: PlanAction; count: number }[] {
  return PLAN_ACTIONS.map((action) => ({ action, count: run.summary?.[action] ?? 0 })).filter((c) => c.count > 0)
}

/** An item that changes nothing and records no ownership: the list hides these until asked. */
export function isQuiet(item: DeclarativeRunItem): boolean {
  return item.action === 'unchanged' && !item.adopt && !item.error
}

/** Items to list: the ones that change something (deletions first), then the quiet ones when `showQuiet`. */
export function listedItems(run: DeclarativeRun, showQuiet: boolean): DeclarativeRunItem[] {
  const rank = (a: PlanAction): number => PLAN_ACTIONS.indexOf(a)
  return run.items
    .filter((i) => showQuiet || !isQuiet(i))
    .map((item, index) => ({ item, index }))
    .sort((a, b) => rank(a.item.action) - rank(b.item.action) || a.index - b.index)
    .map((x) => x.item)
}

/** A value as one line of the change list (mirrors the `oa diff` output): scalars as they are, lists and maps as JSON. */
export function formatValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'string') return value === '' ? '""' : value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return JSON.stringify(value)
}

/** How one field change reads: a value added, removed (a list element) or replaced. */
export type ChangeKind = 'added' | 'removed' | 'replaced' | 'secret'

export function changeKind(change: { old?: unknown; new?: unknown; secret?: boolean; path: string }, action: PlanAction): ChangeKind {
  if (change.secret) return 'secret'
  if (action === 'create' || (change.old == null && change.new != null)) return 'added'
  if (change.new == null && change.path.includes('[') && change.old != null) return 'removed'
  return 'replaced'
}

/** What would be deleted: the items a prune removes, which is what a confirmation has to name. */
export function deletions(run: DeclarativeRun): DeclarativeRunItem[] {
  return run.items.filter((i) => i.action === 'delete')
}

/** True when the plan can be applied: it changes something and has no error that makes it unapplicable. */
export function canApply(run: DeclarativeRun | null): boolean {
  return !!run && !run.code && run.changes && !(run.errors?.length) && !run.items.some((i) => i.error)
}

/** The per-item outcome counts after an apply. */
export function outcomeCounts(run: DeclarativeRun): { applied: number; failed: number; skipped: number } {
  const out = { applied: 0, failed: 0, skipped: 0 }
  for (const item of run.items) if (item.result) out[item.result]++
  return out
}

/** Joins the text of several manifest files into one stream (a `---` line between them), as `oa apply -f a -f b` reads them. */
export function joinManifests(texts: readonly string[]): string {
  return texts.map((t) => t.replace(/\n+$/, '')).join('\n---\n') + '\n'
}
