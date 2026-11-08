// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t, tp, type TranslationKey } from '../i18n'
import type {
  Bundle,
  BundleCheck,
  BundleCheckStatus,
  BundleItemInput,
  BundlePromotion,
  BundleState,
  ItemCandidate,
  Pipeline,
  PromotionStatus,
} from '../api/bundles'
import type { BadgeVariant } from './scanStatus'
import { compareTimestamps } from './datetime'

export function bundleStateVariant(state: BundleState): BadgeVariant {
  switch (state) {
    case 'sealed':
      return 'success'
    case 'archived':
      return 'neutral'
    default:
      return 'warning'
  }
}

export function bundleStateLabel(state: BundleState): string {
  switch (state) {
    case 'sealed':
      return t('bundles.state.sealed')
    case 'archived':
      return t('bundles.state.archived')
    default:
      return t('bundles.state.draft')
  }
}

export function promotionVariant(status: PromotionStatus): BadgeVariant {
  if (status === 'success') return 'success'
  if (status === 'failed') return 'danger'
  return 'warning'
}

export function promotionLabel(status: PromotionStatus): string {
  if (status === 'success') return t('bundles.promotion.success')
  if (status === 'failed') return t('bundles.promotion.failed')
  return t('bundles.promotion.inProgress')
}

/** Stages a sealed bundle can still be promoted to: the ones after its current stage. */
export function promotableStages(pipeline: Pipeline | undefined, bundle: Bundle): Pipeline['stages'] {
  if (!pipeline || bundle.state !== 'sealed') return []
  const at = bundle.current_stage ? pipeline.stages.findIndex((s) => s.name === bundle.current_stage) : -1
  return pipeline.stages.slice(at + 1)
}

/** The one primary action of a bundle: what the page puts first. Archived bundles have none. */
export type BundleAction = 'seal' | 'promote' | 'archive'

/** Why the primary action is not the usual one, or not available; every notice is shown before the action. */
export type BundleNotice =
  /** A draft without items cannot be sealed (blocks the action). */
  | 'noItems'
  /** The bundle was sealed without a signature; one can only be added when sealing. */
  | 'unsigned'
  /** The bundle has no pipeline, so it cannot be promoted. */
  | 'noPipeline'
  /** The bundle's pipeline could not be loaded. */
  | 'pipelineUnavailable'
  /** The bundle reached the last stage of its pipeline. */
  | 'finalStage'
  | 'archived'

export const NOTICE_TEXT: Record<BundleNotice, TranslationKey> = {
  noItems: 'bundles.notice.noItems',
  unsigned: 'bundles.notice.unsigned',
  noPipeline: 'bundles.notice.noPipeline',
  pipelineUnavailable: 'bundles.notice.pipelineUnavailable',
  finalStage: 'bundles.notice.finalStage',
  archived: 'bundles.notice.archived',
}
/** Notices styled as warnings; the others are informational. */
export const NOTICE_WARN = new Set<BundleNotice>(['noItems', 'unsigned', 'pipelineUnavailable'])

export interface BundleStatus {
  primary: BundleAction | null
  /** The primary action cannot run yet. */
  blocked: boolean
  notices: BundleNotice[]
  /** The stage a promotion goes to next. */
  nextStage: Pipeline['stages'][number] | null
}

/**
 * Draft: seal and sign. Sealed: promote to the next stage, or archive once there is none.
 * `itemCount` is null while the items are unknown (not loaded or failed); it blocks nothing then,
 * the server stays authoritative.
 */
export function bundleStatus(bundle: Bundle, pipeline: Pipeline | undefined, itemCount: number | null): BundleStatus {
  if (bundle.state === 'archived') return { primary: null, blocked: false, notices: ['archived'], nextStage: null }

  if (bundle.state === 'draft') {
    const empty = itemCount === 0
    return { primary: 'seal', blocked: empty, notices: empty ? ['noItems'] : [], nextStage: null }
  }

  const notices: BundleNotice[] = bundle.gpg_key_id ? [] : ['unsigned']
  const nextStage = promotableStages(pipeline, bundle)[0] ?? null
  if (nextStage) return { primary: 'promote', blocked: false, notices, nextStage }
  if (!bundle.pipeline_id) notices.push('noPipeline')
  else if (!pipeline) notices.push('pipelineUnavailable')
  else notices.push('finalStage')
  return { primary: 'archive', blocked: false, notices, nextStage: null }
}

export type StepKind = 'draft' | 'sealed' | 'signed' | 'stage'
/** `skipped`: a sealed bundle that carries no signature. */
export type StepStatus = 'done' | 'current' | 'todo' | 'skipped'

export interface LifecycleStep {
  /** Stable key: the kind, or `stage:<name>`. */
  id: string
  kind: StepKind
  /** The stage name for stage steps, the translated step name otherwise. */
  label: string
  status: StepStatus
  at: string | null
  by: string | null
}

/**
 * The bundle's way through the release process: created, sealed, signed, then each stage of its pipeline.
 * Stages up to the current one are done, with the time and person of their latest successful promotion;
 * the first step not done yet is the current one (none once archived).
 */
export function lifecycleSteps(bundle: Bundle, pipeline: Pipeline | undefined, promotions: BundlePromotion[]): LifecycleStep[] {
  const sealed = bundle.state !== 'draft'
  const signed = Boolean(bundle.gpg_key_id)
  const reached = bundle.current_stage ? (pipeline?.stages.findIndex((s) => s.name === bundle.current_stage) ?? -1) : -1

  const steps: LifecycleStep[] = [
    { id: 'draft', kind: 'draft', label: t('bundles.step.draft'), status: 'done', at: bundle.created_at, by: bundle.created_by },
    {
      id: 'sealed',
      kind: 'sealed',
      label: t('bundles.step.sealed'),
      status: sealed ? 'done' : 'todo',
      at: bundle.sealed_at,
      by: bundle.sealed_by,
    },
    {
      id: 'signed',
      kind: 'signed',
      label: t('bundles.step.signed'),
      status: signed ? 'done' : sealed ? 'skipped' : 'todo',
      at: signed ? bundle.sealed_at : null,
      by: signed ? bundle.sealed_by : null,
    },
  ]

  for (const [i, stage] of (pipeline?.stages ?? []).entries()) {
    const done = i <= reached
    const last = promotions
      .filter((p) => p.status === 'success' && p.to_stage === stage.name)
      .sort((a, b) => compareTimestamps(b.started_at, a.started_at))[0]
    steps.push({
      id: `stage:${stage.name}`,
      kind: 'stage',
      label: stage.name,
      status: done ? 'done' : 'todo',
      at: done ? (last?.started_at ?? null) : null,
      by: done ? (last?.promoted_by ?? null) : null,
    })
  }

  if (bundle.state === 'archived') return steps
  const current = steps.find((s) => s.status === 'todo')
  if (current) current.status = 'current'
  return steps
}

/** Name/version rules the server enforces: 1-255 bytes, no `/`, `\` or control characters. */
export function validateBundleLabel(value: string): boolean {
  const v = value
  if (v === '' || new TextEncoder().encode(v).length > 255) return false
  // eslint-disable-next-line no-control-regex
  return !/[/\\\u0000-\u001f\u007f]/.test(v)
}

/** Path of a bundle's detail page. */
export function bundleRoute(b: Pick<Bundle, 'name' | 'version'>): string {
  return `/bundles/${encodeURIComponent(b.name)}/${encodeURIComponent(b.version)}`
}

const SHA256_RE = /^[0-9a-f]{64}$/i

/** A search hit can become an item only when it carries the digest the bundle pins. */
export function isBundleable(c: ItemCandidate): boolean {
  return SHA256_RE.test(c.sha256 ?? '') && Boolean(c.path)
}

/** Stable identity of a candidate: the digest the bundle pins (hits of one blob in two repositories count once). */
export function candidateKey(c: ItemCandidate): string {
  return c.sha256.toLowerCase()
}

/** The add-items request entry of a picked candidate; null when its repository is unknown to the caller. */
export function candidateToItem(c: ItemCandidate, repoIdByName: ReadonlyMap<string, string>): BundleItemInput | null {
  const repoId = repoIdByName.get(c.repository)
  if (!repoId) return null
  const item: BundleItemInput = {
    source_repo_id: repoId,
    sha256: c.sha256.toLowerCase(),
    artifact_path: c.path,
    format: c.format,
  }
  if (Number.isFinite(c.size) && c.size >= 0) item.size_bytes = c.size
  return item
}

/** Limits of a stage's required check names (the server enforces the same ones). */
export const CHECK_NAME_MAX = 64
export const REQUIRED_CHECKS_MAX = 20

const CHECK_NAME_FORBIDDEN = /[\s\p{Cc}]/u

/**
 * Reads the required checks a stage form holds: names separated by commas and/or whitespace. Names are
 * trimmed; an empty text is no checks. `error` is the translated reason the list is refused, else null.
 */
export function parseRequiredChecks(text: string): { names: string[]; error: string | null } {
  const names = text.split(/[,\s]+/).filter((n) => n !== '')
  const seen = new Set<string>()
  for (const name of names) {
    if (CHECK_NAME_FORBIDDEN.test(name)) return { names, error: tp('bundles.pipeline.checks.invalid', { name }) }
    if (name.length > CHECK_NAME_MAX) return { names, error: tp('bundles.pipeline.checks.tooLong', { name, max: CHECK_NAME_MAX }) }
    if (seen.has(name)) return { names, error: tp('bundles.pipeline.checks.duplicate', { name }) }
    seen.add(name)
  }
  if (names.length > REQUIRED_CHECKS_MAX) return { names, error: tp('bundles.pipeline.checks.tooMany', { max: REQUIRED_CHECKS_MAX }) }
  return { names, error: null }
}

export function checkVariant(status: BundleCheckStatus): BadgeVariant {
  switch (status) {
    case 'success':
      return 'success'
    case 'failure':
    case 'error':
      return 'danger'
    default:
      return 'warning'
  }
}

export function checkLabel(status: BundleCheckStatus): string {
  switch (status) {
    case 'success':
      return t('bundles.check.success')
    case 'failure':
      return t('bundles.check.failure')
    case 'error':
      return t('bundles.check.error')
    default:
      return t('bundles.check.pending')
  }
}

/** True when the check has an expiry that is not after `now` (an expired check no longer counts). */
export function checkExpired(c: Pick<BundleCheck, 'expires_at'>, now = Date.now()): boolean {
  if (!c.expires_at) return false
  const at = Date.parse(c.expires_at)
  return Number.isFinite(at) && at <= now
}

/** The details URL when it is http(s), else null: a reported check must not make the page link to javascript: and the like. */
export function safeDetailsUrl(url: string | undefined): string | null {
  if (!url) return null
  try {
    const u = new URL(url)
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : null
  } catch {
    return null
  }
}

/** What a required check currently is for a promotion: its reported status, or why it does not count. */
export type RequiredCheckState = BundleCheckStatus | 'missing' | 'expired'

/** The state of each required check, in the stage's order. Only a `success` that has not expired passes. */
export function requiredCheckStates(required: readonly string[], checks: readonly BundleCheck[], now = Date.now()): { name: string; state: RequiredCheckState }[] {
  return required.map((name) => {
    const found = checks.find((c) => c.name === name)
    if (!found) return { name, state: 'missing' }
    return { name, state: checkExpired(found, now) ? 'expired' : found.status }
  })
}

export function requiredCheckVariant(state: RequiredCheckState): BadgeVariant {
  return state === 'missing' || state === 'expired' ? 'neutral' : checkVariant(state)
}

export function requiredCheckLabel(state: RequiredCheckState): string {
  if (state === 'missing') return t('bundles.check.missing')
  if (state === 'expired') return t('bundles.check.expired')
  return checkLabel(state)
}
