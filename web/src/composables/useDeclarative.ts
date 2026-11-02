// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref, watch } from 'vue'
import {
  applyManifest,
  declarativeExportUrl,
  declarativeRefusal,
  diffManifest,
  exportManifest,
  type DeclarativeRequest,
  type DeclarativeRun,
} from '../api/declarative'
import { downloadAttachment } from '../api/client'
import { canApply, joinManifests } from '../lib/declarativePlan'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

export type DeclarativeBusy = 'diff' | 'apply' | 'export' | null

/**
 * The state of the declarative-config page: the manifest and its options, the plan the server
 * answered for them (`oa diff`), and the outcome of applying it (`oa apply`). Editing the manifest
 * or an option discards the plan: only a plan reviewed for exactly what is in the editor can be applied.
 */
export function useDeclarative(onApplied: () => void) {
  const toasts = useToastStore()

  const manifest = ref('')
  const kinds = ref<string[]>([])
  const prune = ref(false)
  const pruneAll = ref(false)
  const forceSecrets = ref(false)
  const continueOnError = ref(false)
  const withMembers = ref(false)
  /** Values for the `secretRef: {env: NAME}` references, by NAME. */
  const secrets = ref<Record<string, string>>({})

  const plan = ref<DeclarativeRun | null>(null)
  /** The plan above is the outcome of an apply, not a preview. */
  const applied = ref(false)
  const error = ref('')
  const busy = ref<DeclarativeBusy>(null)

  watch([manifest, kinds, prune, pruneAll, forceSecrets, continueOnError], () => {
    plan.value = null
    applied.value = false
    error.value = ''
  })

  const request = (): DeclarativeRequest => ({
    manifest: manifest.value,
    kinds: kinds.value,
    prune: prune.value,
    prune_all: pruneAll.value,
    force_secrets: forceSecrets.value,
    continue_on_error: continueOnError.value,
    secrets: Object.fromEntries(Object.entries(secrets.value).filter(([, v]) => v !== '')),
  })

  const hasManifest = computed(() => manifest.value.trim() !== '')
  const applicable = computed(() => !applied.value && canApply(plan.value))

  /** Runs a call that answers a plan; a refusal that carries one (invalid manifest, missing secret) is shown as a plan too. */
  async function run(kind: 'diff' | 'apply', call: (req: DeclarativeRequest) => Promise<DeclarativeRun>): Promise<boolean> {
    busy.value = kind
    error.value = ''
    try {
      plan.value = await call(request())
      applied.value = kind === 'apply'
      return true
    } catch (err) {
      const refusal = declarativeRefusal(err)
      if (refusal) {
        plan.value = refusal
        error.value = refusal.error ?? ''
      } else {
        plan.value = null
        error.value = err instanceof Error ? err.message : t('sx.declarative.failed')
      }
      applied.value = false
      return false
    } finally {
      busy.value = null
    }
  }

  const review = (): Promise<boolean> => run('diff', diffManifest)

  async function apply(): Promise<void> {
    if (!(await run('apply', applyManifest)) || !plan.value) return
    onApplied()
    const failed = plan.value.items.some((i) => i.result === 'failed')
    if (failed) toasts.warning(t('sx.declarative.applied.failed'))
    else toasts.success(t('sx.declarative.applied'))
  }

  async function readFiles(files: readonly File[]): Promise<void> {
    try {
      const texts = await Promise.all(files.map((f) => f.text()))
      manifest.value = joinManifests(texts)
    } catch (err) {
      error.value = err instanceof Error ? err.message : t('sx.declarative.failed')
    }
  }

  /** Puts the live state in the editor, to edit and apply back. */
  async function loadLive(): Promise<void> {
    busy.value = 'export'
    try {
      manifest.value = await exportManifest(kinds.value, withMembers.value)
    } catch (err) {
      error.value = err instanceof Error ? err.message : t('sx.declarative.failed')
    } finally {
      busy.value = null
    }
  }

  async function download(): Promise<void> {
    busy.value = 'export'
    try {
      await downloadAttachment(declarativeExportUrl(kinds.value, withMembers.value), 'openartifact-manifests.yaml')
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('sx.declarative.failed'))
    } finally {
      busy.value = null
    }
  }

  return {
    manifest, kinds, prune, pruneAll, forceSecrets, continueOnError, withMembers, secrets,
    plan, applied, error, busy, hasManifest, applicable,
    review, apply, readFiles, loadLive, download,
  }
}
