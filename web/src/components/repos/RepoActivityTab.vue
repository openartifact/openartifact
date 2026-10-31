<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  What happened to one repository, newest first, grouped by day: uploads, deletions, trash restores and
  purges, promotions, quarantine and (for repository admins) settings, cleanup runs and federation. The
  server decides what each caller may see; `admin` only decides which event-type filters are offered.
  Keyset paging: the next page loads when the end of the list scrolls into view.
-->
<template>
  <section :aria-label="t('rl.activity.caption')">
    <FilterBar :active="category !== null" @clear="setCategory(null)">
      <div role="group" :aria-label="t('rl.activity.filter')" class="flex flex-wrap items-center gap-1.5" data-testid="activity-filter">
        <button
          v-for="option in options"
          :key="option.value"
          type="button"
          :aria-pressed="option.value === (category ?? '')"
          :data-category="option.value"
          class="inline-flex h-8 items-center rounded-full border px-3 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
          :class="option.value === (category ?? '') ? 'border-primary bg-primary-soft text-primary' : 'border-border-strong text-muted hover:text-foreground'"
          @click="setCategory(option.value === '' ? null : (option.value as RepoActivityCategory))"
        >
          {{ option.label }}
        </button>
      </div>
    </FilterBar>

    <div v-if="loading" class="space-y-2" role="status" aria-busy="true" data-testid="activity-loading">
      <span class="sr-only">{{ t('common.loading') }}</span>
      <SkeletonRows :rows="5" header="h-4 w-24">
        <div class="h-8 w-8 rounded-full skel" />
        <div class="h-4 flex-1 rounded skel" />
        <div class="h-4 w-10 rounded skel" />
      </SkeletonRows>
    </div>

    <StateBlock v-else-if="failed" variant="error" :title="t('rl.activity.loadFailed')" data-testid="activity-error">
      <template #action>
        <AppButton variant="secondary" size="sm" @click="reload">{{ t('browser.retry') }}</AppButton>
      </template>
    </StateBlock>

    <div v-else-if="entries.length === 0" class="rounded-xl border border-border bg-surface" data-testid="activity-empty">
      <StateBlock
        v-if="category !== null"
        variant="filtered"
      >
        <template #action>
          <AppButton variant="secondary" size="sm" @click="setCategory(null)">{{ t('ui.filter.clear') }}</AppButton>
        </template>
      </StateBlock>
      <StateBlock v-else variant="empty" :title="t('rl.activity.empty')" :hint="t('rl.activity.emptyHint')" />
    </div>

    <template v-else>
      <div class="space-y-5">
        <div v-for="group in groups" :key="group.day" data-testid="activity-day">
          <h3 class="mb-2 text-sm font-semibold text-foreground">{{ group.label }}</h3>
          <ol class="divide-y divide-border overflow-hidden rounded-xl border border-border bg-surface">
            <li v-for="e in group.items" :key="e.id" class="flex items-start gap-3 px-4 py-3" data-testid="activity-row">
              <span class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full" :class="toneClasses(visual(e).tone)">
                <component :is="visual(e).icon" class="h-4 w-4" aria-hidden="true" />
              </span>
              <div class="min-w-0 flex-1">
                <p class="flex min-w-0 flex-wrap items-baseline gap-x-2 text-sm">
                  <span class="font-medium text-foreground">{{ actionLabel(e.action) }}</span>
                  <router-link
                    v-if="subjectLink(e)"
                    :to="subjectLink(e)!"
                    class="min-w-0 truncate font-mono text-xs text-foreground hover:text-primary"
                    :title="subjectText(e)"
                  >{{ subjectText(e) }}</router-link>
                  <span v-else-if="subjectText(e)" class="min-w-0 truncate font-mono text-xs text-muted" :title="subjectText(e)">{{ subjectText(e) }}</span>
                </p>
                <p class="mt-0.5 text-xs text-muted">
                  <span v-if="actorText(e)" data-testid="activity-actor">{{ actorText(e) }}</span>
                  <span v-if="actorText(e) && detail(e)" aria-hidden="true"> · </span>
                  <span v-if="detail(e)">{{ detail(e) }}</span>
                </p>
              </div>
              <RelativeTime :value="e.timestamp" class="shrink-0 text-xs text-muted" />
            </li>
          </ol>
        </div>
      </div>

      <div v-if="nextCursor" class="mt-4 flex flex-col items-center gap-2" data-testid="activity-more">
        <p v-if="moreFailed" class="text-sm text-danger" role="alert">{{ t('rl.activity.loadMoreFailed') }}</p>
        <span v-if="loadingMore" class="text-xs text-muted" role="status">{{ t('rl.activity.loadingMore') }}</span>
        <AppButton v-if="moreFailed || !observing" variant="secondary" size="sm" :disabled="loadingMore" data-testid="activity-load-more" @click="loadMore">
          {{ moreFailed ? t('browser.retry') : t('rl.activity.loadMore') }}
        </AppButton>
        <div ref="sentinel" class="h-px w-full" aria-hidden="true" />
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, toRef, watch, nextTick } from 'vue'
import AppButton from '../AppButton.vue'
import FilterBar from '../ui/FilterBar.vue'
import RelativeTime from '../ui/RelativeTime.vue'
import SkeletonRows from '../ui/SkeletonRows.vue'
import StateBlock from '../ui/StateBlock.vue'
import type { RepoActivityEntry } from '../../api/repositories'
import { useRepoActivity } from '../../composables/useRepoActivity'
import { actionLabel } from '../../lib/activity'
import { artifactPath } from '../../lib/artifactPath'
import { actionVisual, entryDetail, groupByDay, toneClasses, visibleCategories, type RepoActivityCategory } from '../../lib/repoActivity'
import { tDynamic, t } from '../../i18n'

const props = defineProps<{
  repo: string
  /** Whether the caller administers the repository: offers the settings, cleanup and federation filters. */
  admin?: boolean
}>()

const { entries, nextCursor, category, loading, loadingMore, failed, moreFailed, reload, loadMore, setCategory } = useRepoActivity(toRef(props, 'repo'))

const options = computed(() => [
  { value: '', label: t('rl.activity.category.all') },
  ...visibleCategories(props.admin === true).map((c) => ({ value: c as string, label: tDynamic(`rl.activity.category.${c}`) })),
])

const groups = computed(() => groupByDay(entries.value))

const visual = (e: RepoActivityEntry) => actionVisual(e.action)
const detail = (e: RepoActivityEntry): string => entryDetail(e)

function actorText(e: RepoActivityEntry): string {
  if (e.actor_name || e.actor) return e.actor_name || e.actor
  return e.actor_type === 'system' ? t('rl.activity.system') : ''
}

function subjectText(e: RepoActivityEntry): string {
  const s = e.subject
  if (!s?.name) return ''
  return s.version ? `${s.name} ${s.version}` : s.name
}

// Packages link to their artifact page; other subjects are plain text.
function subjectLink(e: RepoActivityEntry): string | null {
  const s = e.subject
  return s?.kind === 'package' && s.name ? artifactPath(props.repo, s.name, s.version) : null
}

// Infinite scroll: the sentinel under the list loads the next page when it comes into view. Without
// IntersectionObserver (or after a failed page) the button does it.
const sentinel = ref<HTMLElement | null>(null)
const observing = ref(typeof IntersectionObserver !== 'undefined')
let observer: IntersectionObserver | null = null

function stopObserving(): void {
  observer?.disconnect()
  observer = null
}

watch(
  // A new sentinel element (after every page: the old one stays in view while loading) re-arms the observer.
  () => [sentinel.value, entries.value.length, moreFailed.value] as const,
  async ([el, , failedMore]) => {
    stopObserving()
    await nextTick()
    if (!el || failedMore || !observing.value) return
    observer = new IntersectionObserver((hits) => {
      if (hits.some((h) => h.isIntersecting)) void loadMore()
    })
    observer.observe(el)
  },
  { flush: 'post' },
)

onBeforeUnmount(stopObserving)
</script>
