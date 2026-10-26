<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Ordered member picker of a group repository: the first member that has a match wins. -->
<template>
  <div class="space-y-3">
    <p class="text-xs text-muted">{{ t('repoDetail.group.hint') }}</p>
    <ol v-if="model.length > 0" class="divide-y divide-border rounded-lg border border-border bg-surface" data-testid="members-selected">
      <li v-for="(name, i) in model" :key="name" class="flex min-h-11 items-center gap-2 px-3 py-1.5">
        <span class="w-6 shrink-0 font-mono text-xs text-muted">{{ i + 1 }}</span>
        <span class="min-w-0 flex-1 truncate font-mono text-sm">{{ name }}</span>
        <button type="button" :class="ICON_BTN" :disabled="i === 0" :aria-label="tp('rl.members.moveUp', { name })" @click="move(i, -1)">
          <ArrowUpIcon class="h-4 w-4" aria-hidden="true" />
        </button>
        <button type="button" :class="ICON_BTN" :disabled="i === model.length - 1" :aria-label="tp('rl.members.moveDown', { name })" @click="move(i, 1)">
          <ArrowDownIcon class="h-4 w-4" aria-hidden="true" />
        </button>
        <button type="button" :class="ICON_BTN" :aria-label="tp('rl.members.remove', { name })" @click="remove(i)">
          <XMarkIcon class="h-4 w-4" aria-hidden="true" />
        </button>
      </li>
    </ol>
    <p v-else class="text-sm text-muted">{{ t('rl.members.none') }}</p>

    <div v-if="available.length > 0">
      <p class="mb-1.5 text-xs font-medium text-foreground">{{ t('rl.members.add') }}</p>
      <ul class="flex flex-wrap gap-2" data-testid="members-available">
        <li v-for="name in available" :key="name">
          <button
            type="button"
            class="inline-flex min-h-9 items-center gap-1 rounded-md border border-border-strong bg-surface px-2.5 text-sm font-mono hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:min-h-11"
            @click="model = [...model, name]"
          >
            <PlusIcon class="h-3.5 w-3.5 text-muted" aria-hidden="true" />
            {{ name }}
          </button>
        </li>
      </ul>
    </div>
    <p v-else-if="candidates.length === 0" class="text-sm text-muted">{{ t('repos.new.noMembers') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ArrowDownIcon, ArrowUpIcon, PlusIcon, XMarkIcon } from '@heroicons/vue/20/solid'
import { t, tp } from '../../i18n/en'

const ICON_BTN =
  'inline-flex h-9 w-9 items-center justify-center rounded-md text-muted hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:opacity-40 max-md:h-11 max-md:w-11'

const props = defineProps<{ candidates: string[] }>()
const model = defineModel<string[]>({ required: true })

const available = computed(() => props.candidates.filter((n) => !model.value.includes(n)))

function move(i: number, by: -1 | 1): void {
  const next = [...model.value]
  const [item] = next.splice(i, 1)
  next.splice(i + by, 0, item)
  model.value = next
}

function remove(i: number): void {
  model.value = model.value.filter((_, idx) => idx !== i)
}
</script>
