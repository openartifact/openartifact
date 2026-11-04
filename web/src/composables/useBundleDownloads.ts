// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { manifestUrl, signatureUrl } from '../api/bundles'
import { downloadAttachment } from '../api/client'
import { useToastStore } from '../stores/toast'
import { t } from '../i18n'

/** Downloads of a sealed bundle's manifest and detached signature; a failure is toasted. */
export function useBundleDownloads(name: () => string, version: () => string): {
  downloadManifest: () => Promise<void>
  downloadSignature: () => Promise<void>
} {
  const toasts = useToastStore()

  async function download(url: string, fallback: string): Promise<void> {
    try {
      await downloadAttachment(url, fallback)
    } catch (err) {
      toasts.error(err instanceof Error ? err.message : t('bundles.action.downloadFailed'))
    }
  }
  return {
    downloadManifest: () => download(manifestUrl(name(), version()), `${name()}-${version()}.manifest.json`),
    downloadSignature: () => download(signatureUrl(name(), version()), `${name()}-${version()}.manifest.asc`),
  }
}
