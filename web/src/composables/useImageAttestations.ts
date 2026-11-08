// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, watch } from 'vue'
import { getAttestationStatement, listAttestations, reverifyAttestations, type ImageAttestation } from '../api/attestations'
import { useToastStore } from '../stores/toast'
import { t, tpn } from '../i18n'

interface Source {
  repo: () => string
  image: () => string
  /** `sha256:<hex>` of the image manifest, or null when it is unknown. */
  subject: () => string | null
}

/** The attachments of one image manifest, the lazily loaded statement of one of them, and "verify again". */
export function useImageAttestations(source: Source) {
  const toasts = useToastStore()
  const rows = ref<ImageAttestation[]>([])
  const loading = ref(false)
  const error = ref('')
  const reverifying = ref(false)

  const viewing = ref<ImageAttestation | null>(null)
  const statement = ref('')
  const statementLoading = ref(false)
  const statementError = ref('')

  let generation = 0
  async function load(): Promise<void> {
    const mine = ++generation
    const subject = source.subject()
    error.value = ''
    if (!subject) {
      rows.value = []
      return
    }
    loading.value = true
    try {
      const res = await listAttestations(source.repo(), subject, source.image())
      if (mine === generation) rows.value = res
    } catch (err) {
      if (mine !== generation) return
      rows.value = []
      error.value = err instanceof Error ? err.message : t('artifact.attestations.loadFailed')
    } finally {
      if (mine === generation) loading.value = false
    }
  }

  async function reverify(): Promise<void> {
    const subject = source.subject()
    if (!subject || reverifying.value) return
    reverifying.value = true
    try {
      const n = await reverifyAttestations(source.repo(), subject, source.image())
      toasts.success(tpn('artifact.attestations.reverified', n))
      await load()
    } catch (err) {
      toasts.error(t('artifact.attestations.reverifyFailed'), err instanceof Error ? err.message : undefined)
    } finally {
      reverifying.value = false
    }
  }

  let statementGeneration = 0
  async function view(row: ImageAttestation): Promise<void> {
    const mine = ++statementGeneration
    viewing.value = row
    statement.value = ''
    statementError.value = ''
    statementLoading.value = true
    try {
      const res = await getAttestationStatement(source.repo(), row.digest, source.image())
      if (mine === statementGeneration) statement.value = JSON.stringify(res, null, 2)
    } catch (err) {
      if (mine === statementGeneration) statementError.value = err instanceof Error ? err.message : t('artifact.attestations.statementFailed')
    } finally {
      if (mine === statementGeneration) statementLoading.value = false
    }
  }

  function closeStatement(): void {
    statementGeneration++
    viewing.value = null
  }

  watch(() => [source.repo(), source.image(), source.subject()], load, { immediate: true })

  return { rows, loading, error, reverifying, viewing, statement, statementLoading, statementError, load, reverify, view, closeStatement }
}
