// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ArtifactProperties } from '../api/artifactProperties'

export interface PropertyRow {
  key: string
  value: string
}

/** The rows of the edit form, in key order. */
export function propertyRows(properties: ArtifactProperties): PropertyRow[] {
  return Object.keys(properties)
    .sort()
    .map((key) => ({ key, value: properties[key] ?? '' }))
}

/** The first name used twice (after trimming), or undefined. Rows with an empty name are ignored. */
export function duplicateName(rows: readonly PropertyRow[]): string | undefined {
  const seen = new Set<string>()
  for (const row of rows) {
    const key = row.key.trim()
    if (!key) continue
    if (seen.has(key)) return key
    seen.add(key)
  }
  return undefined
}

export interface PropertyChanges {
  /** Keys that are new or whose value changed. */
  set: ArtifactProperties
  /** Keys that were removed from the form. */
  remove: string[]
}

/** What saving `rows` must send so the stored properties become exactly `rows`. */
export function propertyChanges(before: ArtifactProperties, rows: readonly PropertyRow[]): PropertyChanges {
  const next = new Map<string, string>()
  for (const row of rows) {
    const key = row.key.trim()
    if (key) next.set(key, row.value)
  }
  const set: ArtifactProperties = {}
  for (const [key, value] of next) if (before[key] !== value) set[key] = value
  const remove = Object.keys(before).filter((key) => !next.has(key))
  return { set, remove }
}
