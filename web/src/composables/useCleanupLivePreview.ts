// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { previewCleanupDraft } from '../api/cleanup'
import { previewProjectCleanupDraft } from '../api/projects'
import { cleanupPayload, cleanupRuleValid, type CleanupPolicyForm } from '../lib/cleanupPolicy'
import { t } from '../i18n'

/** Quiet time after the last edit before the preview is requested. */
export const PREVIEW_DEBOUNCE_MS = 400

export type LivePreviewStatus = 'idle' | 'loading' | 'ready' | 'error'

/** What a preview is evaluated against: one repository, or the repositories a project default covers. */
export interface PreviewTarget {
  scope: 'repository' | 'project'
  name: string
}

/** The answer shown: one repository's, or the project's repositories added up. */
export interface LivePreviewResult {
  rows: number
  bytes: number
  truncated: boolean
  /** Repositories the policy covers; only set for a project. */
  repositories?: number
  items: Array<{ id: string; name: string; version: string; repository?: string }>
}

async function requestPreview(target: PreviewTarget, body: Record<string, unknown>, signal: AbortSignal): Promise<LivePreviewResult> {
  if (target.scope === 'repository') return previewCleanupDraft(target.name, body, signal)
  const res = await previewProjectCleanupDraft(target.name, body, signal)
  return {
    rows: res.rows,
    bytes: res.repositories.reduce((n, r) => n + r.bytes, 0),
    truncated: res.repositories.some((r) => r.truncated),
    repositories: res.repositories.length,
    items: res.repositories.flatMap((r) => r.items.map((it) => ({ ...it, repository: r.repository }))),
  }
}

/**
 * Live preview of the cleanup policy being edited: after each change of the
 * rule (debounced) the unsaved policy is evaluated by the server, which removes
 * nothing. A newer edit cancels the request in flight, so a slow answer never
 * replaces a fresher one. While `active` is false (editor closed) nothing runs. The target is a repository or, for the
 * defaults of a project, the project (the answer then adds up its repositories).
 */
export function useCleanupLivePreview(target: Ref<PreviewTarget | null>, form: Ref<CleanupPolicyForm>, active: Ref<boolean>) {
  const result = ref<LivePreviewResult | null>(null)
  const status = ref<LivePreviewStatus>('idle')
  const error = ref('')

  let timer: ReturnType<typeof setTimeout> | undefined
  let controller: AbortController | undefined

  function cancel(): void {
    clearTimeout(timer)
    timer = undefined
    controller?.abort()
    controller = undefined
  }

  async function fetchPreview(tgt: PreviewTarget, body: Record<string, unknown>): Promise<void> {
    const ctl = new AbortController()
    controller = ctl
    status.value = 'loading'
    error.value = ''
    try {
      const res = await requestPreview(tgt, body, ctl.signal)
      if (ctl.signal.aborted) return
      result.value = res
      status.value = 'ready'
    } catch (err) {
      if (ctl.signal.aborted) return
      result.value = null
      error.value = err instanceof Error ? err.message : t('cleanup.dryRun.failed')
      status.value = 'error'
    }
  }

  // The rule fields the server evaluates; name and enabled do not change the answer.
  const key = computed(() => (cleanupRuleValid(form.value) ? JSON.stringify(rulePayload(form.value)) : ''))

  watch(
    [key, active, target],
    () => {
      cancel()
      const tgt = target.value
      if (!active.value || !tgt || !key.value) {
        result.value = null
        error.value = ''
        status.value = 'idle'
        return
      }
      status.value = 'loading'
      const body = rulePayload(form.value)
      timer = setTimeout(() => void fetchPreview(tgt, body), PREVIEW_DEBOUNCE_MS)
    },
    { immediate: true },
  )

  onScopeDispose(cancel)

  return { result, status, error }
}

function rulePayload(f: CleanupPolicyForm): Record<string, unknown> {
  const { name: _name, enabled: _enabled, ...rule } = cleanupPayload(f)
  return rule
}
