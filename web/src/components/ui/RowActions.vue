<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!--
  The actions of one table row: icon buttons whose accessible name is "<action>: <subject>"
  (the tooltip is the bare action), or, when there are more than `inline` of them, the
  first ones plus a "More actions" menu. An icon button is ~36px wide and does not grow
  with the language, which is what keeps the actions column narrow in every locale.
  Put it in the actions cell of a table; buttons report through `action`, links navigate.
  The default slot holds extra controls that are not plain actions (a copy button with its own feedback).
-->
<template>
  <div class="flex items-center justify-end gap-0.5" data-testid="row-actions">
    <slot />
    <template v-for="a in shown" :key="a.key">
      <router-link v-if="a.to && !a.disabled" :to="a.to" :class="buttonClass(a)" :aria-label="nameOf(a.label)" v-tooltip="a.label" :data-testid="`action-${a.key}`">
        <component :is="a.icon" class="h-4 w-4" aria-hidden="true" />
      </router-link>
      <button
        v-else
        type="button"
        :class="buttonClass(a)"
        v-tooltip="a.label"
        :aria-label="nameOf(a.label)"
        :disabled="a.disabled || a.loading"
        :aria-busy="a.loading || undefined"
        :aria-pressed="a.pressed"
        :data-testid="`action-${a.key}`"
        @click="emit('action', a.key)"
      >
        <ArrowPathIcon v-if="a.loading" class="h-4 w-4 animate-spin" aria-hidden="true" />
        <component :is="a.icon" v-else class="h-4 w-4" aria-hidden="true" />
      </button>
    </template>

    <Menu v-if="overflow.length > 0" as="div">
      <MenuButton
        ref="anchor"
        v-tooltip="t('ui.table.moreActions')"
        :class="buttonClass()"
        :aria-label="nameOf(t('ui.table.moreActions'))"
        data-testid="action-more"
      >
        <EllipsisHorizontalIcon class="h-4 w-4" aria-hidden="true" />
      </MenuButton>
      <!-- Teleported and fixed (useFloating): the table's scroll container would clip an absolutely positioned menu. -->
      <Teleport to="body">
        <MenuItems
          ref="items"
          class="z-50 min-w-44 rounded-md border border-border bg-surface py-1 shadow-lg focus:outline-none"
          :style="style"
          data-testid="row-actions-menu"
        >
          <MenuItem v-for="a in overflow" :key="a.key" v-slot="{ active, close }" :disabled="a.disabled || a.loading" as="template">
            <router-link
              v-if="a.to && !a.disabled"
              :to="a.to"
              :class="itemClass(a, active)"
              :data-testid="`action-${a.key}`"
              @click="close"
            >
              <component :is="a.icon" class="h-4 w-4 shrink-0" aria-hidden="true" />{{ a.label }}
            </router-link>
            <button v-else type="button" :class="itemClass(a, active)" :data-testid="`action-${a.key}`" @click="emit('action', a.key)">
              <component :is="a.icon" class="h-4 w-4 shrink-0" aria-hidden="true" />{{ a.label }}
            </button>
          </MenuItem>
        </MenuItems>
      </Teleport>
    </Menu>
  </div>
</template>

<script lang="ts">
import type { Component } from 'vue'
import type { RouteLocationRaw } from 'vue-router'

export interface RowAction {
  /** Reported through `action`; also the test id suffix. */
  key: string
  /** Tooltip, and the name of the action in the "More actions" menu. */
  label: string
  icon: Component
  /** Makes it a link instead of a button. */
  to?: RouteLocationRaw
  tone?: 'danger'
  disabled?: boolean
  loading?: boolean
  /** A toggle (a graph that is shown or not): reported as `aria-pressed`. Inline buttons only. */
  pressed?: boolean
  /** Left out entirely (a permission the viewer lacks). */
  hidden?: boolean
}
</script>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { Menu, MenuButton, MenuItem, MenuItems } from '@headlessui/vue'
import { ArrowPathIcon, EllipsisHorizontalIcon } from '@heroicons/vue/20/solid'
import { useFloating, type ElementRef } from '../../composables/useFloating'
import { vTooltip } from '../../lib/tooltip'
import { t } from '../../i18n'

const props = withDefaults(
  defineProps<{
    actions: RowAction[]
    /** What the row is ("charts"): appended to every accessible name so the buttons of a column stay distinguishable. */
    subject?: string
    /** More actions than this move into the menu; the first `inline - 1` stay visible. */
    inline?: number
  }>(),
  { subject: '', inline: 3 },
)

const emit = defineEmits<{ action: [key: string] }>()

const visible = computed(() => props.actions.filter((a) => !a.hidden))
const split = computed(() => (visible.value.length > props.inline ? props.inline - 1 : visible.value.length))
const shown = computed(() => visible.value.slice(0, split.value))
const overflow = computed(() => visible.value.slice(split.value))

function nameOf(label: string): string {
  return props.subject ? `${label}: ${props.subject}` : label
}

function buttonClass(a?: RowAction): string {
  const tone = a?.tone === 'danger' ? 'text-danger hover:bg-danger-soft' : 'text-muted hover:bg-surface-hover hover:text-foreground'
  return `inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:pointer-events-none disabled:opacity-40 max-md:h-11 max-md:w-11 ${tone}`
}

function itemClass(a: RowAction, active: boolean): string {
  const tone = a.tone === 'danger' ? 'text-danger' : 'text-foreground'
  return `flex min-h-9 w-full items-center gap-2.5 px-3 py-1.5 text-left text-sm max-md:min-h-11 ${tone} ${active ? 'bg-surface-hover' : ''} ${a.disabled ? 'pointer-events-none opacity-40' : ''}`
}

// The menu is fixed to the viewport next to its button (right edges flush); it follows the button while the page or the table scrolls.
const anchor = ref<ElementRef>(null)
const items = ref<ElementRef>(null)
const { style } = useFloating(anchor, items, { placement: 'bottom-end' })
</script>
