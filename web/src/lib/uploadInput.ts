// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { TranslationKey } from '../i18n'

/** A picked file the browser cannot turn into a request; `key` is the catalog key of the message shown on its row. */
export class UploadInputError extends Error {
  readonly key: TranslationKey

  constructor(key: TranslationKey) {
    super(key)
    this.key = key
  }
}
