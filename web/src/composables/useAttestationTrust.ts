// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onMounted, ref } from 'vue'
import { getAttestationTrust, reverifyAttestations, setAttestationTrust } from '../api/attestations'
import { formToTrust, trustToForm, type TrustForm } from '../lib/attestations'
import { useToastStore } from '../stores/toast'
import { t, tpn } from '../i18n'

const empty = (): TrustForm => ({ keys: [], roots: [], identities: [], requireProvenance: false })

/** The attestation trust of one docker repository: load, edit, save (the server's validation message is kept) and re-verify. */
export function useAttestationTrust(repo: () => string) {
  const toasts = useToastStore()
  const form = ref<TrustForm>(empty())
  const initial = ref(JSON.stringify(form.value))
  const loading = ref(true)
  const loadError = ref('')
  const saving = ref(false)
  const saveError = ref('')
  const reverifying = ref(false)

  const dirty = computed(() => JSON.stringify(form.value) !== initial.value)

  function adopt(next: TrustForm): void {
    form.value = next
    initial.value = JSON.stringify(next)
  }

  async function load(): Promise<void> {
    loading.value = true
    loadError.value = ''
    try {
      adopt(trustToForm(await getAttestationTrust(repo())))
    } catch (err) {
      loadError.value = err instanceof Error ? err.message : t('repos.trust.loadFailed')
    } finally {
      loading.value = false
    }
  }

  async function save(): Promise<void> {
    if (saving.value) return
    saving.value = true
    saveError.value = ''
    try {
      adopt(trustToForm(await setAttestationTrust(repo(), formToTrust(form.value))))
      toasts.success(t('repos.trust.saved'))
    } catch (err) {
      saveError.value = err instanceof Error ? err.message : t('repos.trust.saveFailed')
    } finally {
      saving.value = false
    }
  }

  async function reverify(): Promise<void> {
    if (reverifying.value) return
    reverifying.value = true
    try {
      toasts.success(tpn('artifact.attestations.reverified', await reverifyAttestations(repo())))
    } catch (err) {
      toasts.error(t('artifact.attestations.reverifyFailed'), err instanceof Error ? err.message : undefined)
    } finally {
      reverifying.value = false
    }
  }

  onMounted(load)

  return { form, dirty, loading, loadError, saving, saveError, reverifying, load, save, reverify }
}
