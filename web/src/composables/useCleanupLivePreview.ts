// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { previewCleanupDraft, type CleanupPreview } from '../api/cleanup'
import { cleanupPayload, cleanupRuleValid, type CleanupPolicyForm } from '../lib/cleanupPolicy'
import { t } from '../i18n'

/** Quiet time after the last edit before the preview is requested. */
export const PREVIEW_DEBOUNCE_MS = 400

export type LivePreviewStatus = 'idle' | 'loading' | 'ready' | 'error'

/**
 * Live preview of the cleanup policy being edited: after each change of the
 * rule (debounced) the unsaved policy is evaluated by the server, which removes
 * nothing. A newer edit cancels the request in flight, so a slow answer never
 * replaces a fresher one. While `active` is false (editor closed) nothing runs.
 */
export function useCleanupLivePreview(repo: Ref<string>, form: Ref<CleanupPolicyForm>, active: Ref<boolean>) {
  const result = ref<CleanupPreview | null>(null)
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

  async function fetchPreview(body: Record<string, unknown>): Promise<void> {
    const ctl = new AbortController()
    controller = ctl
    status.value = 'loading'
    error.value = ''
    try {
      const res = await previewCleanupDraft(repo.value, body, ctl.signal)
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
    [key, active, repo],
    () => {
      cancel()
      if (!active.value || !key.value) {
        result.value = null
        error.value = ''
        status.value = 'idle'
        return
      }
      status.value = 'loading'
      const body = rulePayload(form.value)
      timer = setTimeout(() => void fetchPreview(body), PREVIEW_DEBOUNCE_MS)
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
