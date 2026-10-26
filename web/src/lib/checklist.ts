// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/** Real counts behind the first-run checklist; null/undefined when the caller cannot read them. */
export interface ChecklistCounts {
  repositories?: number | null
  /** Packages that exist in repositories the caller can read. */
  packages?: number | null
  tokens?: number | null
  /** At least one scanner reports ready. */
  scannerReady?: boolean | null
  users?: number | null
}

export type ChecklistStepId = 'repository' | 'publish' | 'token' | 'scanner' | 'team'

/** One step; its texts live under `dashboard.step.<id>.*` in the catalog. */
export interface ChecklistStep {
  id: ChecklistStepId
  done: boolean
  /** Where the step is done. `#connect` scrolls to the Connect a client block. */
  to: string
  params: Record<string, string>
}

const known = (n: number | null | undefined): n is number => typeof n === 'number'

/**
 * The steps of the checklist for a role, each done or not from real counts. A step whose
 * count is unknown is left out rather than shown as open: the list never invents progress.
 */
export function deriveChecklist(isAdmin: boolean, c: ChecklistCounts): ChecklistStep[] {
  const steps: ChecklistStep[] = []
  const add = (id: ChecklistStepId, done: boolean, to: string, params: Record<string, string> = {}): void => {
    steps.push({ id, done, to, params })
  }

  if (isAdmin && known(c.repositories)) add('repository', c.repositories > 0, '/repos?new=1', { count: String(c.repositories) })
  if (known(c.packages)) add('publish', c.packages > 0, '#connect', { count: String(c.packages) })
  if (known(c.tokens)) add('token', c.tokens > 0, '/settings/tokens?new=1', { count: String(c.tokens) })
  if (isAdmin && typeof c.scannerReady === 'boolean') add('scanner', c.scannerReady, '/settings/scanners')
  if (isAdmin && known(c.users)) add('team', c.users > 1, '/settings/users', { count: String(c.users) })
  return steps
}

/** A brand new instance: nothing to publish into, or nothing published yet. */
export function isFreshInstance(c: Pick<ChecklistCounts, 'repositories' | 'packages'>): boolean {
  return c.repositories === 0 || c.packages === 0
}

export function progress(steps: readonly ChecklistStep[]): { done: number; total: number } {
  return { done: steps.filter((s) => s.done).length, total: steps.length }
}
