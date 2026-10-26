// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { getCurrentScope, onScopeDispose, ref, shallowRef, readonly } from 'vue'
import { ApiError } from '../api/client'
import { QUERY_TOO_BROAD, search } from '../api/search'
import { searchRepositories, searchUsers } from '../api/paletteSearch'
import { listProjects } from '../api/projects'
import { listBundles } from '../api/bundles'
import { EMPTY_ENTITIES, type EntityResults, type PaletteItem } from '../lib/palette'

/**
 * - `idle`: nothing requested.
 * - `loading`: a query waits for its debounce or for at least one source.
 * - `ready`: every source answered.
 * - `tooBroad`: the package search refused the query (the other sources are still shown).
 * - `error`: at least one source failed; `retry()` repeats the query.
 */
type PaletteSearchStatus = 'idle' | 'loading' | 'ready' | 'tooBroad' | 'error'

interface PaletteSearchOptions {
  debounceMs?: number
  /** Hits per source. */
  limit?: number
  /** Minimum characters before the API is asked. */
  minChars?: number
  /** Whether the caller may use the admin-only sources (bundles, users). */
  isAdmin: () => boolean
}

const enc = encodeURIComponent

/** Package names may contain slashes (npm scopes, image paths, Go modules): keep them as path separators. */
function packagePath(repo: string, name: string): string {
  return `/repos/${enc(repo)}/packages/${name.split('/').map(enc).join('/')}`
}

/**
 * Debounced entity search for the command palette. The static results (pages,
 * settings, actions) never wait for it: each source applies its own answer as
 * it arrives. A newer query aborts the requests of the older one, and an answer
 * that raced its own abort (or belongs to an older query) is dropped, so a slow
 * earlier response can never replace a newer result.
 */
export function usePaletteSearch(options: PaletteSearchOptions) {
  const debounceMs = options.debounceMs ?? 200
  const limit = options.limit ?? 5
  const minChars = options.minChars ?? 2

  const status = ref<PaletteSearchStatus>('idle')
  const entities = shallowRef<EntityResults>(EMPTY_ENTITIES)

  let query = ''
  let generation = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let controller: AbortController | undefined
  // The projects list has no server-side filter: fetched once per palette
  // session and filtered as the text changes.
  let projectsLoad: Promise<PaletteItem[]> | undefined

  function cancel(): void {
    clearTimeout(timer)
    timer = undefined
    controller?.abort()
    controller = undefined
    generation++
  }

  function apply(gen: number, patch: Partial<EntityResults>): void {
    if (gen === generation) entities.value = { ...entities.value, ...patch }
  }

  function loadProjects(): Promise<PaletteItem[]> {
    projectsLoad ??= listProjects().then((res) =>
      res.projects.flatMap((p): PaletteItem[] =>
        p.name
          ? [{
              id: `project:${p.name}`,
              group: 'projects',
              title: p.display_name || p.name,
              description: p.display_name ? p.name : (p.description ?? undefined),
              keywords: p.name,
              icon: 'projects',
              to: `/projects/${enc(p.name)}`,
            }]
          : [],
      ),
    )
    projectsLoad.catch(() => {
      projectsLoad = undefined
    })
    return projectsLoad
  }

  async function run(): Promise<void> {
    timer = undefined
    const gen = generation
    const own = new AbortController()
    controller = own
    const { signal } = own
    const q = query
    let failed = false
    let tooBroad = false

    const sources: Promise<void>[] = [
      searchRepositories(q, limit, signal).then((repos) =>
        apply(gen, {
          repositories: repos.map((r) => ({
            id: `repo:${r.name}`,
            group: 'repositories',
            title: r.name,
            description: [r.repo_type, r.description].filter(Boolean).join(' · ') || undefined,
            icon: 'repos',
            format: r.format,
            mono: true,
            to: `/repos/${enc(r.name)}`,
          })),
        }),
      ),
      search({ q, limit, group: 'package', signal }).then((res) =>
        apply(gen, {
          packages: res.packages.map((p) => ({
            id: `package:${p.repository}:${p.name}`,
            group: 'packages',
            title: p.name,
            description: `${p.version} · ${p.repository}`,
            icon: 'packages',
            format: p.format,
            mono: true,
            to: packagePath(p.repository, p.name),
          })),
        }),
      ).catch((err: unknown) => {
        if (err instanceof ApiError && err.code === QUERY_TOO_BROAD) tooBroad = true
        else throw err
      }),
      loadProjects().then((items) => apply(gen, { projects: items })),
    ]
    if (options.isAdmin()) {
      sources.push(
        listBundles({ name: q, limit }).then((res) =>
          apply(gen, {
            bundles: (res.bundles ?? []).map((b) => ({
              id: `bundle:${b.name}:${b.version}`,
              group: 'bundles',
              title: `${b.name} ${b.version}`,
              description: b.state,
              keywords: b.name,
              icon: 'bundles',
              mono: true,
              to: `/bundles/${enc(b.name)}/${enc(b.version)}`,
            })),
          }),
        ),
        searchUsers(q, limit, signal).then((users) =>
          apply(gen, {
            users: users.flatMap((u): PaletteItem[] =>
              u.username
                ? [{
                    id: `user:${u.username}`,
                    group: 'users',
                    title: u.username,
                    description: u.email,
                    icon: 'user',
                    to: `/settings/users?q=${enc(u.username)}`,
                  }]
                : [],
            ),
          }),
        ),
      )
    }

    // A source that failed leaves the others in place; an abort is not a failure.
    await Promise.all(
      sources.map((s) =>
        s.catch(() => {
          if (!signal.aborted) failed = true
        }),
      ),
    )
    if (signal.aborted || gen !== generation) return
    status.value = failed ? 'error' : tooBroad ? 'tooBroad' : 'ready'
  }

  /** Sets the text to search for; text below the minimum clears the entity results. */
  function setQuery(text: string, checksum = false): void {
    cancel()
    query = text.trim()
    entities.value = EMPTY_ENTITIES
    if (query.length < minChars || checksum || query.startsWith('>')) {
      status.value = 'idle'
      return
    }
    status.value = 'loading'
    timer = setTimeout(() => void run(), debounceMs)
  }

  /** Repeats the current query at once, e.g. after an error. */
  function retry(): void {
    if (query.length < minChars) return
    cancel()
    status.value = 'loading'
    void run()
  }

  /** Forgets everything of the closed palette, including the cached projects. */
  function reset(): void {
    setQuery('')
    projectsLoad = undefined
  }

  if (getCurrentScope()) onScopeDispose(cancel)

  return { status: readonly(status), entities, setQuery, retry, reset }
}
