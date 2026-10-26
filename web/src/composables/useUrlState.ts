// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, type ComputedRef } from 'vue'
import { useRoute, useRouter, type LocationQuery, type LocationQueryValue } from 'vue-router'

/**
 * How one query parameter is read from and written to the URL. `serialize`
 * returns `null` to drop the parameter (the default value never reaches the URL).
 */
export interface Codec<T> {
  default: T
  parse(raw: string | null): T
  serialize(value: T): string | null
}

export type Schema = Record<string, Codec<unknown>>
export type Values<S extends Schema> = { [K in keyof S]: S[K] extends Codec<infer T> ? T : never }

/** A free-text parameter. */
export function strParam(def = ''): Codec<string> {
  return {
    default: def,
    parse: (raw) => (raw === null || raw === '' ? def : raw),
    serialize: (v) => (v === def || v === '' ? null : v),
  }
}

/** An integer parameter; values below `min` or non-numeric input fall back to the default. */
export function intParam(def: number, min = 1): Codec<number> {
  return {
    default: def,
    parse: (raw) => {
      if (raw === null || !/^-?\d+$/.test(raw)) return def
      const n = Number(raw)
      return n >= min ? n : def
    },
    serialize: (v) => (v === def ? null : String(v)),
  }
}

/** A `1` / absent flag. */
export function boolParam(def = false): Codec<boolean> {
  return {
    default: def,
    parse: (raw) => (raw === null ? def : raw === '1' || raw === 'true'),
    serialize: (v) => (v === def ? null : v ? '1' : '0'),
  }
}

/** One of a fixed set of values; anything else falls back to the default. */
export function enumParam<T extends string>(values: readonly T[], def: T): Codec<T> {
  return {
    default: def,
    parse: (raw) => (raw !== null && (values as readonly string[]).includes(raw) ? (raw as T) : def),
    serialize: (v) => (v === def ? null : v),
  }
}

/** A comma-separated list (e.g. `?type=vuln,secret`). */
export function listParam(def: readonly string[] = []): Codec<string[]> {
  const key = (a: readonly string[]) => a.join(',')
  return {
    default: [...def],
    parse: (raw) => (raw === null || raw === '' ? [...def] : raw.split(',').filter(Boolean)),
    serialize: (v) => (key(v) === key(def) || v.length === 0 ? null : v.join(',')),
  }
}

function first(v: LocationQueryValue | LocationQueryValue[] | undefined): string | null {
  const one = Array.isArray(v) ? v[0] : v
  return one ?? null
}

/** Parses every key of `schema` out of a query object. */
export function readQuery<S extends Schema>(schema: S, query: LocationQuery): Values<S> {
  const out: Record<string, unknown> = {}
  for (const key of Object.keys(schema)) out[key] = schema[key].parse(first(query[key]))
  return out as Values<S>
}

/** Merges `patch` into `query`: schema keys are re-serialized (defaults dropped), other keys are untouched. */
export function writeQuery<S extends Schema>(
  schema: S,
  query: LocationQuery,
  patch: Partial<Values<S>>,
): LocationQuery {
  const next: LocationQuery = { ...query }
  for (const key of Object.keys(patch)) {
    const codec = schema[key]
    if (!codec) continue
    const serialized = codec.serialize((patch as Record<string, unknown>)[key])
    if (serialized === null) delete next[key]
    else next[key] = serialized
  }
  return next
}

export interface UrlState<S extends Schema> {
  /** The typed parameters, restored from the URL on every navigation (back/forward, deep link). */
  values: ComputedRef<Values<S>>
  /** Updates parameters. Filters use `replace` (the default); pass `push` for navigations that belong in history. */
  set(patch: Partial<Values<S>>, mode?: 'replace' | 'push'): Promise<void>
  /** Resets the given keys (default: all) to their default values. */
  reset(keys?: (keyof S)[], mode?: 'replace' | 'push'): Promise<void>
}

/**
 * Typed query-string state. Parameters outside the schema (`?tab=`, `?new=1`) are
 * preserved, defaults are never written to the URL, and filter updates use
 * `replaceState` so they do not flood the history.
 */
export function useUrlState<S extends Schema>(schema: S): UrlState<S> {
  const route = useRoute()
  const router = useRouter()
  const values = computed(() => readQuery(schema, route.query))

  async function set(patch: Partial<Values<S>>, mode: 'replace' | 'push' = 'replace'): Promise<void> {
    const query = writeQuery(schema, router.currentRoute.value.query, patch)
    await (mode === 'push' ? router.push({ query }) : router.replace({ query }))
  }

  function reset(keys?: (keyof S)[], mode: 'replace' | 'push' = 'replace'): Promise<void> {
    const patch: Record<string, unknown> = {}
    for (const key of keys ?? Object.keys(schema)) patch[key as string] = schema[key as string].default
    return set(patch as Partial<Values<S>>, mode)
  }

  return { values, set, reset }
}
