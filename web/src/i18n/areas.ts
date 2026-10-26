// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * The catalog files of every locale: `locales/<tag>/<area>.json`.
 * `bun run i18n:check` fails on a file under locales/ that is not listed here.
 */
export const AREAS = [
  'admin',
  'artifact',
  'auth',
  'branding',
  'bundles',
  'core',
  'dashboard',
  'federation',
  'projects',
  'repolist',
  'repos',
  'search',
  'security',
  'settings',
  'settingsx',
  'shell',
  'tiering',
  'tokens',
  'ui',
] as const

export type Area = (typeof AREAS)[number]
