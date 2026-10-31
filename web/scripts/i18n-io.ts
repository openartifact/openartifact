// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Reading and writing the catalogs for scripts/i18n-missing.ts and scripts/i18n-apply.ts.

import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { AREAS } from '../src/i18n/areas'
import type { LocaleFiles } from '../src/i18n/catalogCheck'
import type { Messages } from '../src/i18n/translate'

export const localesDir = join(import.meta.dir, '..', 'src', 'i18n', 'locales')

export function loadLocale(tag: string): LocaleFiles {
  const dir = join(localesDir, tag)
  const files: Record<string, Messages> = {}
  if (!existsSync(dir)) return files
  for (const name of readdirSync(dir)) {
    const area = name.replace(/\.json$/, '')
    if (name.endsWith('.json') && (AREAS as readonly string[]).includes(area)) {
      files[area] = JSON.parse(readFileSync(join(dir, name), 'utf8')) as Messages
    }
  }

  return files
}

/** Same format as the committed catalogs: 2-space indent, UTF-8 as is, trailing newline. */
export function writeCatalog(tag: string, area: string, messages: Messages): void {
  writeFileSync(join(localesDir, tag, `${area}.json`), `${JSON.stringify(messages, null, 2)}\n`)
}
