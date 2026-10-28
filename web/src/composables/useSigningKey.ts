// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, onMounted, ref } from 'vue'
import { ApiError } from '../api/client'
import { useToastStore } from '../stores/toast'

/** The body of a key replacement: both fields are optional, an empty body generates a fresh key. */
export interface SigningKeyInput {
  private_key?: string
  key_name?: string
}

/**
 * State and actions shared by the signing key panels (apk, nix): load the key, and generate or import a
 * replacement through a dialog. `messages` are the panel's own texts; `nameRule` is the key name pattern the
 * server also enforces.
 */
export function useSigningKey<K>(opts: {
  messages: { loadFailed: () => string; replaced: () => string; replacedHint: () => string; replaceFailed: () => string }
  nameRule: RegExp
  get: () => Promise<K>
  replace: (body: SigningKeyInput) => Promise<K>
}) {
  const toasts = useToastStore()

  const key = ref<K | null>(null)
  const loading = ref(true)
  const loadError = ref('')
  // The server cannot seal a private key without OPENARTIFACT_SECRET_KEY: no key can be generated or imported.
  const secretRequired = ref(false)

  const open = ref(false)
  const keyName = ref('')
  const privateKey = ref('')
  const saving = ref(false)
  const saveError = ref('')
  const nameValid = computed(() => keyName.value === '' || opts.nameRule.test(keyName.value))

  async function load(): Promise<void> {
    loading.value = true
    loadError.value = ''
    secretRequired.value = false
    try {
      key.value = (await opts.get()) as typeof key.value
    } catch (err) {
      secretRequired.value = err instanceof ApiError && err.status === 409 && err.code === 'secret_key_required'
      loadError.value = err instanceof Error ? err.message : opts.messages.loadFailed()
    } finally {
      loading.value = false
    }
  }
  onMounted(load)

  function close(): void {
    if (saving.value) return
    open.value = false
    saveError.value = ''
    privateKey.value = ''
    keyName.value = ''
  }

  async function replace(): Promise<void> {
    const body: SigningKeyInput = {}
    if (privateKey.value.trim()) body.private_key = privateKey.value.trim()
    if (keyName.value) body.key_name = keyName.value
    saving.value = true
    saveError.value = ''
    try {
      key.value = (await opts.replace(body)) as typeof key.value
      loadError.value = ''
      saving.value = false
      close()
      toasts.success(opts.messages.replaced(), opts.messages.replacedHint())
    } catch (err) {
      saveError.value = err instanceof Error ? err.message : opts.messages.replaceFailed()
    } finally {
      saving.value = false
    }
  }

  return { key, loading, loadError, secretRequired, open, keyName, privateKey, saving, saveError, nameValid, close, replace }
}
