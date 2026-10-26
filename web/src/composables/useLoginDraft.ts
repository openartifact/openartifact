// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref } from 'vue'

// Module state, not component state: the app re-creates every page when the
// language changes, and a half-typed sign-in must survive that. Memory only,
// never storage, and cleared once the sign-in succeeds.
const draft = ref({ username: '', password: '' })

export function useLoginDraft() {
  function clearDraft(): void {
    draft.value = { username: '', password: '' }
  }

  return { draft, clearDraft }
}
