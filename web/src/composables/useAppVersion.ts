// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, type Ref } from 'vue'
import { getSystemInfo } from '../api/admin'

// The server version is public (/api/v1/system/info) and fixed for the lifetime
// of the process, so it is fetched once and shared by every consumer.
const version = ref<string | null>(null)
let inflight: Promise<void> | null = null

function load(): Promise<void> {
  inflight ??= getSystemInfo()
    .then((info) => {
      version.value = info?.version || null
    })
    .catch(() => {
      // Unknown version is rendered as such; allow a later consumer to retry.
      inflight = null
    })
  return inflight
}

export function useAppVersion(): Ref<string | null> {
  void load()
  return version
}
