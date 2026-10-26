<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  `/settings/:section`: resolves the id through the registry and hosts that section's page next to a
  sibling rail. The section page body renders under a PageHeader owned here (breadcrumb
  Settings > Group > Section, title and description from the registry); sections put their header
  buttons in <SectionActions>.
-->
<template>
  <div v-if="!section">
    <PageHeader :title="t('ui.settings.unknown')" :crumbs="[{ label: t('ui.settings.hub'), to: '/settings' }, { label: t('ui.settings.unknown') }]" />
    <div class="rounded-lg border border-border bg-surface">
      <StateBlock variant="empty" :title="t('ui.settings.unknown')" :hint="t('ui.settings.unknownHint')">
        <template #action>
          <AppButton variant="secondary" to="/settings">{{ t('settings.page.allSettings') }}</AppButton>
        </template>
      </StateBlock>
    </div>
  </div>

  <div v-else class="grid grid-cols-1 items-start gap-x-8 md:grid-cols-[14rem_minmax(0,1fr)]">
    <!-- desktop rail: the open group first and expanded, the others collapsed -->
    <nav class="hidden md:sticky md:top-4 md:block" :aria-label="t('shell.settingsSections')" data-testid="settings-rail">
      <router-link to="/settings" class="mb-3 inline-flex items-center gap-1.5 text-sm text-muted hover:text-foreground">
        <ArrowLeftIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('settings.page.allSettings') }}
      </router-link>
      <details v-for="g in rail" :key="g.id" :open="g.current" class="group mb-1" :data-testid="`rail-group-${g.id}`">
        <summary class="flex min-h-9 cursor-pointer list-none items-center justify-between rounded px-2 text-sm font-semibold text-foreground hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary">
          {{ t(g.titleKey) }}
          <ChevronDownIcon class="h-4 w-4 text-muted transition-transform group-open:rotate-180" aria-hidden="true" />
        </summary>
        <ul class="mt-0.5 space-y-0.5">
          <li v-for="s in g.sections" :key="s.id">
            <router-link
              :to="sectionPath(s.id, isAdmin)"
              class="flex min-h-9 items-center gap-2 rounded px-2 text-sm hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              :class="s.id === section.id ? 'bg-primary-soft font-medium text-primary' : 'text-foreground/80'"
              :aria-current="s.id === section.id ? 'page' : undefined"
            >
              <component :is="settingsIcon(s.id)" class="h-4 w-4 shrink-0" aria-hidden="true" />
              {{ t(s.titleKey) }}
            </router-link>
          </li>
        </ul>
      </details>
    </nav>

    <div class="min-w-0">
      <!-- mobile: back link and a select instead of the rail -->
      <div class="mb-3 md:hidden">
        <router-link to="/settings" class="mb-2 inline-flex min-h-11 items-center gap-1.5 text-sm text-muted hover:text-foreground">
          <ArrowLeftIcon class="h-4 w-4" aria-hidden="true" />
          {{ t('settings.page.allSettings') }}
        </router-link>
        <label for="settings-jump" class="sr-only">{{ t('settings.page.jumpTo') }}</label>
        <select
          id="settings-jump"
          :value="section.id"
          class="h-11 w-full rounded-lg border border-border-strong bg-surface px-3 text-sm text-foreground"
          data-testid="settings-jump"
          @change="jump(($event.target as HTMLSelectElement).value)"
        >
          <optgroup v-for="g in rail" :key="g.id" :label="t(g.titleKey)">
            <option v-for="s in g.sections" :key="s.id" :value="s.id">{{ t(s.titleKey) }}</option>
          </optgroup>
        </select>
      </div>

      <PageHeader :title="t(section.titleKey)" :description="t(section.descKey)" :crumbs="crumbs">
        <template #actions>
          <div ref="actionsEl" class="flex flex-wrap items-center gap-2" data-testid="section-actions" />
        </template>
      </PageHeader>

      <div v-if="forbidden" class="rounded-lg border border-border bg-surface" data-testid="section-forbidden">
        <StateBlock
          variant="forbidden"
          :title="tp('settings.page.forbidden.title', { title: t(section.titleKey) })"
          :hint="tp('settings.page.forbidden.hint', { user: session.user?.username ?? '' })"
        >
          <template #action>
            <AppButton variant="secondary" to="/settings">{{ t('settings.page.allSettings') }}</AppButton>
          </template>
        </StateBlock>
      </div>
      <component :is="page" v-else :key="`${section.id}:${nonce}`" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, defineComponent, h, provide, ref, shallowRef } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeftIcon, ChevronDownIcon } from '@heroicons/vue/20/solid'
import AppButton from '../../components/AppButton.vue'
import PageHeader from '../../components/PageHeader.vue'
import StateBlock from '../../components/ui/StateBlock.vue'
import PageLoadingFallback from '../../components/PageLoadingFallback.vue'
import { settingsIcon } from '../../components/settingsIcons'
import type { Crumb } from '../../lib/crumbs'
import { sectionPath } from '../../lib/navigation'
import { SETTINGS_ACTIONS_TARGET } from '../../lib/settingsActions'
import { groupOf, siblingRail } from '../../lib/settingsSearch'
import { findSettingsSection, SETTINGS_GROUPS } from '../../lib/settingsSections'
import { useSessionStore } from '../../stores/session'
import { t, tp } from '../../i18n'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const isAdmin = computed(() => session.isAdmin)

const section = computed(() => findSettingsSection(route.params.section))
const forbidden = computed(() => !!section.value?.adminOnly && !isAdmin.value)
const rail = computed(() => (section.value ? siblingRail(section.value.id, isAdmin.value) : []))

const crumbs = computed<Crumb[]>(() => {
  const s = section.value
  if (!s) return []
  const group = SETTINGS_GROUPS.find((g) => g.id === groupOf(s, isAdmin.value))
  return [
    { label: t('ui.settings.hub'), to: '/settings' },
    ...(group ? [{ label: t(group.titleKey) }] : []),
    { label: t(s.titleKey) },
  ]
})

const actionsEl = shallowRef<HTMLElement | null>(null)
provide(SETTINGS_ACTIONS_TARGET, actionsEl)

// Retry after a failed chunk download: a new nonce builds a new async component.
const nonce = ref(0)
const LoadFailed = defineComponent({
  setup: () => () =>
    h('div', { class: 'rounded-lg border border-border bg-surface', 'data-testid': 'section-load-failed' }, [
      h(
        StateBlock,
        { variant: 'error', title: t('settings.page.loadFailed.title'), hint: t('settings.page.loadFailed.hint') },
        { action: () => h(AppButton, { variant: 'secondary', onClick: () => nonce.value++ }, () => t('settings.page.retry')) },
      ),
    ]),
})

const page = computed(() => {
  void nonce.value
  const s = section.value
  if (!s) return null
  const loader = isAdmin.value && s.loadAdmin ? s.loadAdmin : s.load
  return defineAsyncComponent({ loader, loadingComponent: PageLoadingFallback, errorComponent: LoadFailed, delay: 200 })
})

function jump(id: string): void {
  void router.push(sectionPath(id, isAdmin.value))
}
</script>
