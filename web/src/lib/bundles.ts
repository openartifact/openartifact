// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { t } from '../i18n/en'
import type { Bundle, BundleState, BundleItemInput, Pipeline, PromotionStatus } from '../api/bundles'
import type { BadgeVariant } from './scanStatus'

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

export type StageStatus = 'done' | 'current' | 'pending'

interface StageStep {
  name: string
  repoId?: string
  status: StageStatus
}

/**
 * The pipeline as a progress trail. Stages up to and including the bundle's
 * `current_stage` are done (the current one is highlighted); with no current
 * stage every stage is pending.
 */
export function stageSteps(pipeline: Pipeline | undefined, currentStage: string | null): StageStep[] {
  if (!pipeline) return []
  const at = currentStage ? pipeline.stages.findIndex((s) => s.name === currentStage) : -1
  return pipeline.stages.map((s, i) => ({
    name: s.name,
    repoId: s.repo_id || undefined,
    status: i < at ? 'done' : i === at ? 'current' : 'pending',
  }))
}

/** Stages a sealed bundle can still be promoted to: the ones after its current stage. */
export function promotableStages(pipeline: Pipeline | undefined, bundle: Bundle): Pipeline['stages'] {
  if (!pipeline || bundle.state !== 'sealed') return []
  const at = bundle.current_stage ? pipeline.stages.findIndex((s) => s.name === bundle.current_stage) : -1
  return pipeline.stages.slice(at + 1)
}

const SHA256_RE = /^[0-9a-f]{64}$/i

export interface ItemFormValues {
  source_repo_id: string
  sha256: string
  artifact_path: string
  format: string
  size_bytes: string
}

export type ItemFormErrors = Partial<Record<'source_repo_id' | 'sha256' | 'artifact_path' | 'size_bytes', string>>

/** Client-side check mirroring the server rules; the server stays authoritative. */
export function validateItemForm(v: ItemFormValues): ItemFormErrors {
  const errors: ItemFormErrors = {}
  if (!v.source_repo_id) errors.source_repo_id = t('bundles.items.errRepo')
  if (!SHA256_RE.test(v.sha256.trim())) errors.sha256 = t('bundles.items.errSha')
  const path = v.artifact_path.trim()
  if (!path) errors.artifact_path = t('bundles.items.errPath')
  else if (new TextEncoder().encode(path).length > 1024) errors.artifact_path = t('bundles.items.errPathLong')
  const size = v.size_bytes.trim()
  if (size !== '' && !/^\d+$/.test(size)) errors.size_bytes = t('bundles.items.errSize')
  return errors
}

export function itemFormToInput(v: ItemFormValues): BundleItemInput {
  const input: BundleItemInput = {
    source_repo_id: v.source_repo_id,
    sha256: v.sha256.trim().toLowerCase(),
    artifact_path: v.artifact_path.trim(),
  }
  if (v.format) input.format = v.format
  if (v.size_bytes.trim() !== '') input.size_bytes = Number(v.size_bytes.trim())
  return input
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
