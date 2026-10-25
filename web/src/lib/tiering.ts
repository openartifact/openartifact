// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Pure helpers behind the storage tiering screens: unit conversion between
// what the admin types (days, KB..TB) and what the API stores (seconds,
// bytes), the policy form model and the plain-words condition summary.

import type { StoragePolicy, StoragePolicyInput } from '../api/tiering'
import { t, type TranslationKey } from '../i18n/en'
import { FORMAT_LOCALE } from './datetime'
import { formatExactDuration } from './duration'
import { formatBytes } from './quota'

export const SECONDS_PER_DAY = 86_400

export const TIER_SIZE_UNITS = [
  { label: 'KB', bytes: 1024 },
  { label: 'MB', bytes: 1024 ** 2 },
  { label: 'GB', bytes: 1024 ** 3 },
  { label: 'TB', bytes: 1024 ** 4 },
] as const

type TierSizeUnit = (typeof TIER_SIZE_UNITS)[number]['label']

/** What a number input yields: `v-model.number` gives '' for an empty field. */
export type NumInput = number | string | null | undefined

function positive(v: NumInput): number | null {
  if (v === null || v === undefined || v === '') return null
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) && n > 0 ? n : null
}

/** Days typed by the admin to seconds; empty or non-positive means "no condition" (null). */
export function daysToSeconds(days: NumInput): number | null {
  const n = positive(days)
  return n === null ? null : Math.round(n * SECONDS_PER_DAY)
}

/** Inverse of {@link daysToSeconds}, rounded to four decimals. */
export function secondsToDays(seconds: number | null | undefined): number | null {
  if (!seconds || seconds <= 0) return null
  return Math.round((seconds / SECONDS_PER_DAY) * 10_000) / 10_000
}

export interface SizeField {
  value: NumInput
  unit: TierSizeUnit
}

function unitBytes(unit: TierSizeUnit): number {
  return (TIER_SIZE_UNITS.find((u) => u.label === unit) ?? TIER_SIZE_UNITS[2]).bytes
}

/** Size typed with a unit selector to bytes; empty or non-positive means "no bound" (null). */
export function sizeToBytes(value: NumInput, unit: TierSizeUnit): number | null {
  const n = positive(value)
  return n === null ? null : Math.round(n * unitBytes(unit))
}

/** Splits bytes into the largest unit that keeps the value >= 1. */
export function bytesToSize(bytes: number | null | undefined): SizeField {
  if (!bytes || bytes <= 0) return { value: null, unit: 'GB' }
  const unit = [...TIER_SIZE_UNITS].reverse().find((u) => bytes >= u.bytes) ?? TIER_SIZE_UNITS[0]
  return { value: Math.round((bytes / unit.bytes) * 100) / 100, unit: unit.label }
}

/** Bytes as a share of a total, 0..100; 0 when the total is empty. */
export function sharePercent(bytes: number | undefined, total: number): number {
  if (!bytes || total <= 0) return 0
  return Math.min(100, (bytes / total) * 100)
}

/** Editable draft of a policy; numbers stay in the units the admin types. */
export interface PolicyForm {
  name: string
  enabled: boolean
  /** Evaluation order; empty on create appends after the last policy. */
  position: NumInput
  targetTier: string
  /** '' = blobs on any tier. */
  fromTier: string
  ageDays: NumInput
  idleDays: NumInput
  minSize: SizeField
  maxSize: SizeField
  repoGlob: string
  formats: string[]
  pathGlob: string
  /**
   * Stored values the form was opened with. A field the admin did not touch is
   * sent back verbatim, so a policy set through the API with sub-day or
   * non-round values is not silently rewritten by an unrelated edit.
   */
  orig: { ageSeconds: number | null; idleSeconds: number | null; minBytes: number | null; maxBytes: number | null }
}

export function emptyPolicyForm(targetTier = ''): PolicyForm {
  return {
    name: '',
    enabled: true,
    position: null,
    targetTier,
    fromTier: '',
    ageDays: null,
    idleDays: null,
    minSize: { value: null, unit: 'GB' },
    maxSize: { value: null, unit: 'GB' },
    repoGlob: '',
    formats: [],
    pathGlob: '',
    orig: { ageSeconds: null, idleSeconds: null, minBytes: null, maxBytes: null },
  }
}

export function formFromPolicy(p: StoragePolicy): PolicyForm {
  return {
    name: p.name ?? '',
    enabled: p.enabled ?? true,
    position: p.position ?? null,
    targetTier: p.target_tier ?? '',
    fromTier: p.from_tier ?? '',
    ageDays: secondsToDays(p.min_age_seconds),
    idleDays: secondsToDays(p.min_idle_seconds),
    minSize: bytesToSize(p.min_size_bytes),
    maxSize: bytesToSize(p.max_size_bytes),
    repoGlob: p.repo_glob ?? '',
    formats: [...(p.formats ?? [])],
    pathGlob: p.path_glob ?? '',
    orig: {
      ageSeconds: p.min_age_seconds ?? null,
      idleSeconds: p.min_idle_seconds ?? null,
      minBytes: p.min_size_bytes ?? null,
      maxBytes: p.max_size_bytes ?? null,
    },
  }
}

function seconds(days: NumInput, orig: number | null): number | null {
  return orig !== null && secondsToDays(orig) === positive(days) ? orig : daysToSeconds(days)
}

function bytes(field: SizeField, orig: number | null): number | null {
  const shown = bytesToSize(orig)
  const same = orig !== null && shown.unit === field.unit && positive(shown.value) === positive(field.value)
  return same ? orig : sizeToBytes(field.value, field.unit)
}

/** The request body for a draft; empty conditions become null so a PUT clears them. */
export function formToInput(f: PolicyForm): StoragePolicyInput {
  const position = f.position === null || f.position === undefined || f.position === '' ? NaN : Math.round(Number(f.position))
  return {
    name: f.name.trim(),
    enabled: f.enabled,
    ...(position >= 0 ? { position } : {}),
    target_tier: f.targetTier,
    from_tier: f.fromTier || null,
    min_age_seconds: seconds(f.ageDays, f.orig.ageSeconds),
    min_idle_seconds: seconds(f.idleDays, f.orig.idleSeconds),
    min_size_bytes: bytes(f.minSize, f.orig.minBytes),
    max_size_bytes: bytes(f.maxSize, f.orig.maxBytes),
    repo_glob: f.repoGlob.trim() || null,
    formats: [...f.formats],
    path_glob: f.pathGlob.trim() || null,
  }
}

/** True when the draft carries at least one condition (the server rejects a policy that would move every blob). */
export function hasCondition(input: StoragePolicyInput): boolean {
  return Boolean(
    input.from_tier ||
      input.min_age_seconds ||
      input.min_idle_seconds ||
      input.min_size_bytes ||
      input.max_size_bytes ||
      input.repo_glob ||
      input.path_glob ||
      input.formats?.length,
  )
}

/** First problem that blocks saving or simulating a draft, as an i18n key; null when it is valid. */
export function policyFormError(f: PolicyForm): TranslationKey | null {
  const input = formToInput(f)
  if (!input.name) return 'tiering.policy.error.name'
  if (!input.target_tier) return 'tiering.policy.error.target'
  if (!hasCondition(input)) return 'tiering.policy.error.condition'
  if (input.min_size_bytes && input.max_size_bytes && input.min_size_bytes > input.max_size_bytes) {
    return 'tiering.policy.error.sizeRange'
  }
  return null
}

/** Plain-words conditions of a saved policy (formats are rendered as badges by the caller). */
export function policyConditions(p: StoragePolicy): string[] {
  const out: string[] = []
  if (p.min_age_seconds) out.push(t('tiering.cond.age').replace('{d}', formatExactDuration(p.min_age_seconds)))
  if (p.min_idle_seconds) out.push(t('tiering.cond.idle').replace('{d}', formatExactDuration(p.min_idle_seconds)))
  const min = p.min_size_bytes ? formatBytes(p.min_size_bytes) : null
  const max = p.max_size_bytes ? formatBytes(p.max_size_bytes) : null
  if (min && max) out.push(t('tiering.cond.sizeRange').replace('{min}', min).replace('{max}', max))
  else if (min) out.push(t('tiering.cond.sizeMin').replace('{min}', min))
  else if (max) out.push(t('tiering.cond.sizeMax').replace('{max}', max))
  if (p.repo_glob) out.push(t('tiering.cond.repo').replace('{glob}', p.repo_glob))
  if (p.path_glob) out.push(t('tiering.cond.path').replace('{glob}', p.path_glob))
  return out
}

/** Integer with locale grouping. */
export function formatCount(n: number | undefined): string {
  return (n ?? 0).toLocaleString(FORMAT_LOCALE)
}
