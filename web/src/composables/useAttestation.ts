// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onMounted, ref } from 'vue'
import { getAttestationAttach, getAttestationKey, setAttestationAttach, type AttestationKey } from '../api/attestation'
import { listAllRepositories } from '../api/repositories'
import { attestationProblem, type AttestationProblem } from '../lib/attestation'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

export interface AttachRow {
  name: string
  /** Undefined while the setting could not be read. */
  enabled: boolean | undefined
  /** The setting is being changed. */
  busy: boolean
}

/** The attestation signing key, and the per-repository switch that attaches signed attestations to docker images. */
export function useAttestation() {
  const toasts = useToastStore()

  const key = ref<AttestationKey | null>(null)
  const problem = ref<AttestationProblem | null>(null)
  const message = ref('')
  const loading = ref(true)
  const rows = ref<AttachRow[]>([])
  const rowsError = ref('')

  // Bumped by every `load`: an answer that arrives after a newer load started is dropped.
  let generation = 0

  async function loadRows(mine: number): Promise<void> {
    rowsError.value = ''
    try {
      const names = (await listAllRepositories({ type: 'hosted', format: 'docker' })).map((r) => r.name)
      const states = await Promise.allSettled(names.map((n) => getAttestationAttach(n)))
      if (mine !== generation) return
      rows.value = names.map((name, i) => {
        const s = states[i]!
        return { name, enabled: s.status === 'fulfilled' ? s.value : undefined, busy: false }
      })
    } catch (err) {
      if (mine !== generation) return
      rows.value = []
      rowsError.value = err instanceof Error ? err.message : t('sx.attestation.attach.error')
    }
  }

  async function load(): Promise<void> {
    const mine = ++generation
    loading.value = true
    problem.value = null
    message.value = ''
    try {
      const loaded = await getAttestationKey()
      if (mine !== generation) return
      key.value = loaded
    } catch (err) {
      if (mine !== generation) return
      key.value = null
      problem.value = attestationProblem(err)
      message.value = err instanceof Error ? err.message : ''
    }
    if (key.value) await loadRows(mine)
    if (mine === generation) loading.value = false
  }
  onMounted(load)

  async function toggle(row: AttachRow, enabled: boolean): Promise<void> {
    if (row.busy) return
    row.busy = true
    try {
      row.enabled = await setAttestationAttach(row.name, enabled)
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('sx.attestation.attach.saveFailed'))
    } finally {
      row.busy = false
    }
  }

  const attaching = computed(() => rows.value.filter((r) => r.enabled).length)

  return { key, problem, message, loading, rows, rowsError, attaching, load, toggle }
}
