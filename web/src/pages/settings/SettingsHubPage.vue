<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Settings hub: one searchable index of every setting, grouped by the job it does.
  Search covers title, keywords, description and id (`?q=`). Everything an administrator
  sees is also listed for everybody else, disabled and with the reason.
-->
<template>
  <div>
    <PageHeader :title="t('ui.settings.hub')" :description="isAdmin ? t('settings.hub.subtitleAdmin') : t('settings.hub.subtitleUser')" />

    <form class="mb-3 max-w-2xl" role="search" @submit.prevent>
      <label for="hub-q" class="mb-1.5 block text-sm font-semibold text-foreground">{{ t('settings.hub.searchLabel') }}</label>
      <div class="relative">
        <MagnifyingGlassIcon class="pointer-events-none absolute left-3 top-1/2 h-5 w-5 -translate-y-1/2 text-muted" aria-hidden="true" />
        <input
          id="hub-q"
          ref="input"
          v-model="draft"
          type="search"
          autocomplete="off"
          enterkeyhint="search"
          :placeholder="isAdmin ? t('settings.hub.searchPlaceholder') : t('settings.hub.searchPlaceholderUser')"
          aria-describedby="hub-count"
          class="h-11 w-full rounded-lg border border-border-strong bg-surface py-1 pl-10 pr-3 text-base text-foreground placeholder:text-muted focus:ring-2 focus:ring-primary"
          @keydown.esc="clearSearch"
        />
      </div>
    </form>
    <div class="mb-5 flex flex-wrap items-center gap-1.5 text-xs text-muted">
      <span>{{ t('settings.hub.examples') }}</span>
      <button
        v-for="e in examples"
        :key="e"
        type="button"
        class="rounded-full border border-border px-2.5 py-1 text-foreground hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:min-h-11 max-md:px-3.5"
        @click="pick(e)"
      >{{ e }}</button>
    </div>
    <p id="hub-count" class="sr-only" role="status" aria-live="polite">{{ liveText }}</p>

    <!-- search results -->
    <template v-if="query">
      <section v-if="results.length" aria-labelledby="hub-results-h" data-testid="hub-results">
        <h2 id="hub-results-h" class="text-sm font-semibold text-foreground">
          {{ tp(results.length === 1 ? 'settings.hub.resultsCount.one' : 'settings.hub.resultsCount.other', { count: String(results.length), q: query }) }}
        </h2>
        <p class="mb-2 text-xs text-muted">{{ t('settings.hub.rankedHint') }}</p>
        <ul class="divide-y divide-border rounded-lg border border-border bg-surface">
          <li v-for="m in results" :key="m.section.id">
            <router-link :to="`/settings/${m.section.id}`" class="flex min-h-11 items-start gap-3 px-4 py-3 hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary">
              <component :is="settingsIcon(m.section.id)" class="mt-0.5 h-5 w-5 shrink-0 text-muted" aria-hidden="true" />
              <span class="min-w-0">
                <span class="block text-sm font-medium text-foreground"><HighlightedText :text="t(m.section.titleKey)" :range="m.titleRange" /></span>
                <span class="block text-xs text-muted"><HighlightedText :text="t(m.section.descKey)" :range="m.descRange" /></span>
                <span class="mt-0.5 block text-xs text-muted">{{ matchLine(m) }}</span>
              </span>
            </router-link>
          </li>
        </ul>
      </section>
      <div v-else class="rounded-lg border border-border bg-surface" data-testid="hub-empty">
        <StateBlock variant="empty" :title="tp('settings.hub.empty.title', { q: query })" :hint="t('settings.hub.empty.hint')">
          <template #action>
            <AppButton variant="secondary" @click="pick('storage')">{{ t('settings.hub.empty.searchStorage') }}</AppButton>
            <AppButton variant="ghost" @click="ui.openCommandPalette()">{{ t('settings.hub.empty.openPalette') }}</AppButton>
          </template>
        </StateBlock>
      </div>
      <p v-if="lockedMatches > 0" class="mt-4 flex items-start gap-2 rounded-lg border border-border bg-surface px-4 py-3 text-sm text-muted" data-testid="hub-locked-matches">
        <InformationCircleIcon class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
        {{ tp(lockedMatches === 1 ? 'settings.hub.lockedMatches.one' : 'settings.hub.lockedMatches.other', { count: String(lockedMatches) }) }}
      </p>
    </template>

    <!-- grouped index -->
    <template v-else>
      <section v-for="g in groups" :key="g.id" class="mb-6" :aria-labelledby="`hub-g-${g.id}`" :data-testid="`hub-group-${g.id}`">
        <h2 :id="`hub-g-${g.id}`" class="text-sm font-semibold text-foreground">{{ t(g.titleKey) }}</h2>
        <p class="mb-2 text-xs text-muted">{{ t(g.descKey) }}</p>
        <ul class="grid gap-px overflow-hidden rounded-lg border border-border bg-border md:grid-cols-2">
          <li v-for="s in g.available" :key="s.id" class="bg-surface">
            <router-link :to="sectionPath(s.id, isAdmin)" class="flex h-full min-h-11 items-start gap-3 px-4 py-3 hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary">
              <component :is="settingsIcon(s.id)" class="mt-0.5 h-5 w-5 shrink-0 text-muted" aria-hidden="true" />
              <span class="min-w-0">
                <span class="block text-sm font-medium text-foreground">{{ t(s.titleKey) }}</span>
                <span class="block text-xs text-muted">{{ t(s.descKey) }}</span>
              </span>
            </router-link>
          </li>
          <li v-for="s in g.locked" :key="s.id" class="bg-surface" data-testid="hub-locked">
            <div class="flex h-full min-h-11 items-start gap-3 px-4 py-3 opacity-70" aria-disabled="true">
              <LockClosedIcon class="mt-0.5 h-5 w-5 shrink-0 text-muted" aria-hidden="true" />
              <span class="min-w-0">
                <span class="block text-sm font-medium text-foreground">{{ t(s.titleKey) }}</span>
                <span class="block text-xs text-muted">{{ t('shell.requiresAdmin') }}</span>
              </span>
            </div>
          </li>
        </ul>
      </section>
      <p v-if="!isAdmin" class="flex items-start gap-2 rounded-lg border border-border bg-surface px-4 py-3 text-sm text-muted" data-testid="hub-managed-note">
        <InformationCircleIcon class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
        {{ t('shell.settingsManagedByAdmins') }}
      </p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { InformationCircleIcon, LockClosedIcon, MagnifyingGlassIcon } from '@heroicons/vue/24/outline'
import AppButton from '../../components/AppButton.vue'
import PageHeader from '../../components/PageHeader.vue'
import StateBlock from '../../components/ui/StateBlock.vue'
import HighlightedText from '../../components/HighlightedText.vue'
import { settingsIcon } from '../../components/settingsIcons'
import { sectionPath } from '../../lib/navigation'
import { hubGroups, lockedMatchCount, openableSections, searchSettings, type SettingsMatch } from '../../lib/settingsSearch'
import { useSessionStore } from '../../stores/session'
import { useUIStore } from '../../stores/ui'
import { t, tp, type TranslationKey } from '../../i18n/en'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const ui = useUIStore()
const input = ref<HTMLInputElement | null>(null)

const isAdmin = computed(() => session.isAdmin)
const groups = computed(() => hubGroups(isAdmin.value))
const examples = computed(() => (isAdmin.value ? ['sso', 'retention', 's3 bucket'] : ['password', 'ci', 'email']))

// The URL holds the committed query; the input shows the draft, which is written back on every keystroke.
const query = computed(() => {
  const raw = route.query.q
  return (Array.isArray(raw) ? raw[0] : raw)?.trim() ?? ''
})
const draft = ref(query.value)
watch(query, (q) => {
  if (q !== draft.value.trim()) draft.value = q
})
watch(draft, (v) => {
  const q = v.trim()
  if (q === query.value) return
  const next = { ...route.query }
  if (q) next.q = q
  else delete next.q
  void router.replace({ query: next })
})

const results = computed(() => searchSettings(query.value, openableSections(isAdmin.value)))
const lockedMatches = computed(() => lockedMatchCount(query.value, isAdmin.value))
const liveText = computed(() =>
  query.value
    ? tp(results.value.length === 1 ? 'settings.hub.resultsCount.one' : 'settings.hub.resultsCount.other', {
        count: String(results.value.length),
        q: query.value,
      })
    : '',
)

function matchLine(m: SettingsMatch): string {
  const fields = m.fields.map((f) => t(`settings.hub.field.${f}` as TranslationKey))
  const kw = m.keywords.length && m.fields.includes('keyword') ? ` (${m.keywords.join(', ')})` : ''
  return tp('settings.hub.matchesIn', { q: query.value, fields: fields.join(', ') + kw })
}

function pick(q: string): void {
  draft.value = q
  input.value?.focus()
}
function clearSearch(): void {
  if (draft.value) draft.value = ''
}

// Desktop only: on touch devices focusing would open the keyboard over the page.
onMounted(() => {
  if (window.matchMedia?.('(min-width: 768px) and (pointer: fine)').matches) input.value?.focus()
})
</script>
