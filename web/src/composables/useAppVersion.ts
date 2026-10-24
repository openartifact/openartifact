// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ref, type Ref } from 'vue'
import { getSystemInfo } from '../api/admin'

// The server version and the matching docs URL are public (/api/v1/system/info)
// and fixed for the lifetime of the process, so they are fetched once and shared
// by every consumer.
const version = ref<string | null>(null)
const docsUrl = ref<string | null>(null)
let inflight: Promise<void> | null = null

function load(): Promise<void> {
  inflight ??= getSystemInfo()
    .then((info) => {
      version.value = info?.version || null
      // The value is rendered as an href: anything but http(s) is dropped.
      docsUrl.value = info?.docs_url && /^https?:\/\//i.test(info.docs_url) ? info.docs_url : null
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

/**
 * Base URL (with trailing slash) of the published docs for this server's version,
 * or null while unknown or when the server has docs links disabled. Every docs
 * link in the UI is built from this and nothing else (see `lib/docs.ts`).
 */
export function useDocsUrl(): Ref<string | null> {
  void load()
  return docsUrl
}

/** Forgets the shared info so the next consumer fetches it again. Tests only. */
export function resetAppInfo(): void {
  inflight = null
  version.value = null
  docsUrl.value = null
}

/** Display form of a server version: `v1.2.3`, or `dev` for development builds. */
export function formatVersion(version: string): string {
  return version === 'dev' ? 'dev' : `v${version}`
}
