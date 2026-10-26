<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  Tabs bound to `?tab=`. Tab changes use pushState, so the back button steps
  through them; the first tab is the default and keeps the URL clean. Panels
  render on demand: only the active tab's slot is invoked (`keep-alive` keeps
  the ones already visited mounted).

  <AppTabs :tabs="tabs" :label="...">
    <template #panel-overview>...</template>
    <template #panel-files>...</template>
  </AppTabs>
-->
<template>
  <div>
    <div
      role="tablist"
      :aria-label="label"
      class="-mt-1 mb-6 flex items-center gap-1 overflow-x-auto border-b border-border"
      @keydown="onKeydown"
    >
      <button
        v-for="tab in visible"
        :id="tabId(tab.id)"
        :key="tab.id"
        :ref="(el) => setRef(tab.id, el)"
        type="button"
        role="tab"
        :aria-selected="tab.id === active"
        :aria-controls="tab.id === active ? panelId(tab.id) : undefined"
        :aria-disabled="tab.disabled ? 'true' : undefined"
        :aria-describedby="tab.disabled ? `${uid}-reason-${tab.id}` : undefined"
        :tabindex="tab.id === active ? 0 : -1"
        :title="tab.disabled ? (tab.disabledReason ?? t('ui.tabs.disabled')) : undefined"
        class="relative inline-flex h-10 shrink-0 items-center gap-1.5 px-3 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11"
        :class="tabClass(tab)"
        @click="choose(tab)"
      >
        {{ tab.label }}
        <span
          v-if="tab.count !== undefined"
          class="rounded px-1.5 py-0.5 font-mono text-micro"
          :class="tab.id === active ? 'bg-primary-soft text-primary' : 'bg-surface-hover text-muted'"
        >{{ tab.count }}</span>
        <span v-if="tab.flag" class="rounded bg-danger-soft px-1.5 py-0.5 text-micro font-medium text-danger">{{ tab.flag }}</span>
        <span v-if="tab.disabled" :id="`${uid}-reason-${tab.id}`" class="sr-only">{{ tab.disabledReason ?? t('ui.tabs.disabled') }}</span>
        <span
          v-if="tab.id === active"
          class="absolute -bottom-px left-2 right-2 h-[2px] rounded-full bg-primary"
          aria-hidden="true"
        />
      </button>
    </div>

    <template v-for="tab in panels" :key="tab.id">
      <div
        v-show="tab.id === active"
        :id="panelId(tab.id)"
        role="tabpanel"
        :aria-labelledby="tabId(tab.id)"
        tabindex="0"
        class="focus-visible:outline-none"
      >
        <slot :name="`panel-${tab.id}`" :tab="tab" :active="active" />
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, useId, watch, type ComponentPublicInstance } from 'vue'
import { useUrlTab, type TabDef } from '../../composables/useUrlTab'
import { t } from '../../i18n'

const props = withDefaults(
  defineProps<{
    tabs: TabDef[]
    /** Accessible name of the tab list. */
    label: string
    /** Query parameter name. */
    param?: string
    /** Other query keys that survive a tab change. */
    keep?: string[]
    /** Keep panels that were already visited mounted (hidden) instead of unmounting them. */
    keepAlive?: boolean
  }>(),
  { param: 'tab', keep: () => [], keepAlive: false },
)

const emit = defineEmits<{ change: [id: string] }>()

const uid = useId()
const visible = computed(() => props.tabs.filter((tab) => !tab.hidden))
const { active, select } = useUrlTab(() => props.tabs, { param: props.param, keep: props.keep })

const visited = ref(new Set<string>())
watch(active, (id) => visited.value.add(id), { immediate: true })
const panels = computed(() =>
  visible.value.filter((tab) => tab.id === active.value || (props.keepAlive && visited.value.has(tab.id))),
)

const tabId = (id: string) => `${uid}-tab-${id}`
const panelId = (id: string) => `${uid}-panel-${id}`

function tabClass(tab: TabDef): string {
  if (tab.disabled) return 'cursor-not-allowed text-muted opacity-60'
  return tab.id === active.value ? 'text-foreground' : 'text-muted hover:text-foreground'
}

async function choose(tab: TabDef): Promise<void> {
  if (tab.disabled || tab.id === active.value) return
  await select(tab.id)
  emit('change', tab.id)
}

const refs = new Map<string, HTMLElement>()
function setRef(id: string, el: Element | ComponentPublicInstance | null): void {
  if (el instanceof HTMLElement) refs.set(id, el)
  else refs.delete(id)
}

// Roving tabindex: arrows move between tabs and select them (disabled tabs take focus only).
function onKeydown(e: KeyboardEvent): void {
  const ids = visible.value.map((tab) => tab.id)
  const current = ids.findIndex((id) => refs.get(id) === document.activeElement)
  if (current < 0) return
  let next = current
  if (e.key === 'ArrowRight') next = (current + 1) % ids.length
  else if (e.key === 'ArrowLeft') next = (current - 1 + ids.length) % ids.length
  else if (e.key === 'Home') next = 0
  else if (e.key === 'End') next = ids.length - 1
  else return
  e.preventDefault()
  const tab = visible.value[next]
  refs.get(tab.id)?.focus()
  void choose(tab)
}
</script>
