// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { computed, ref, shallowRef, watch, type ComputedRef, type Ref } from 'vue'
import { getBundle, listPipelines, listPromotions, type Bundle, type BundlePromotion, type Pipeline } from '../api/bundles'
import { ApiError } from '../api/client'
import { listAllRepositories, type Repository } from '../api/repositories'
import { t } from '../i18n'

export type BundleLoadState = 'loading' | 'ready' | 'not-found' | 'error'

export interface BundleDetail {
  state: Ref<BundleLoadState>
  error: Ref<string>
  bundle: Ref<Bundle | null>
  promotions: Ref<BundlePromotion[]>
  /** The bundle's own pipeline; undefined without one (or when the pipelines could not be loaded). */
  pipeline: ComputedRef<Pipeline | undefined>
  repos: Ref<Repository[]>
  repoName(id: string): string
  /** Loads again without dropping what is shown (after an action). */
  reload(): Promise<void>
}

/**
 * The bundle a detail page shows, with its promotion history, pipeline and the repositories its ids
 * refer to. Pipelines and repositories only decorate the page, so a failure there leaves them empty
 * instead of failing the page.
 */
export function useBundleDetail(name: () => string, version: () => string): BundleDetail {
  const state = ref<BundleLoadState>('loading')
  const error = ref('')
  const bundle = shallowRef<Bundle | null>(null)
  const promotions = shallowRef<BundlePromotion[]>([])
  const pipelines = shallowRef<Pipeline[]>([])
  const repos = shallowRef<Repository[]>([])
  let generation = 0

  async function load(): Promise<void> {
    const mine = ++generation
    try {
      const [b, proms, pls, rs] = await Promise.all([
        getBundle(name(), version()),
        listPromotions(name(), version()),
        listPipelines().catch(() => [] as Pipeline[]),
        listAllRepositories().catch(() => [] as Repository[]),
      ])
      if (mine !== generation) return
      bundle.value = b
      promotions.value = proms
      pipelines.value = pls
      repos.value = rs
      state.value = 'ready'
      error.value = ''
    } catch (err) {
      if (mine !== generation) return
      bundle.value = null
      if (err instanceof ApiError && err.status === 404) {
        state.value = 'not-found'
      } else {
        state.value = 'error'
        error.value = err instanceof Error ? err.message : t('bundles.detailLoadFailed')
      }
    }
  }

  // A different bundle (palette, history) starts from scratch; the same one keeps showing while it reloads.
  watch(
    [name, version],
    () => {
      state.value = 'loading'
      bundle.value = null
      void load()
    },
    { immediate: true },
  )

  const pipeline = computed(() => pipelines.value.find((p) => p.id === bundle.value?.pipeline_id))

  return {
    state,
    error,
    bundle,
    promotions,
    pipeline,
    repos,
    repoName: (id) => repos.value.find((r) => r.id === id)?.name ?? id,
    reload: load,
  }
}
