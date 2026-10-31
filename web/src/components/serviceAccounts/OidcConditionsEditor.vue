<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<!-- Rows of claim + patterns for a trust policy: all rows must hold, one pattern of a row is enough. -->
<template>
  <fieldset class="flex flex-col gap-2" data-testid="conditions-editor">
    <legend class="text-sm font-medium text-foreground">{{ t('sa.cond.title') }}</legend>
    <p class="text-xs text-muted">{{ t('sa.cond.hint') }}</p>
    <div
      v-for="(row, i) in rows"
      :key="i"
      class="flex flex-col gap-2 rounded-lg border border-border p-3"
      :data-testid="`condition-${i}`"
    >
      <div class="flex items-end gap-2">
        <div class="flex min-w-0 flex-1 flex-col gap-1">
          <label :for="`cond-claim-${i}`" class="text-xs font-medium text-muted">{{ t('sa.cond.claim') }}</label>
          <input
            :id="`cond-claim-${i}`"
            :value="row.claim"
            type="text"
            autocomplete="off"
            spellcheck="false"
            placeholder="repository"
            :class="[INPUT_CLASS, 'font-mono']"
            @input="patch(i, { claim: ($event.target as HTMLInputElement).value })"
          />
        </div>
        <button
          type="button"
          class="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-muted hover:bg-surface-hover hover:text-danger focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary max-md:h-11 max-md:w-11"
          :aria-label="tp('sa.cond.remove', { n: String(i + 1) })"
          :title="tp('sa.cond.remove', { n: String(i + 1) })"
          :data-testid="`condition-remove-${i}`"
          @click="remove(i)"
        >
          <TrashIcon class="h-4 w-4" aria-hidden="true" />
        </button>
      </div>
      <div class="flex flex-col gap-1">
        <label :for="`cond-patterns-${i}`" class="text-xs font-medium text-muted">{{ t('sa.cond.patterns') }}</label>
        <textarea
          :id="`cond-patterns-${i}`"
          :value="row.patterns"
          rows="2"
          spellcheck="false"
          :placeholder="t('sa.cond.patternsPlaceholder')"
          :class="[INPUT_CLASS, 'font-mono']"
          @input="patch(i, { patterns: ($event.target as HTMLTextAreaElement).value })"
        />
      </div>
    </div>
    <div>
      <AppButton variant="secondary" size="sm" :disabled="rows.length >= MAX_CONDITIONS" data-testid="condition-add" @click="add">
        <PlusIcon class="h-4 w-4" aria-hidden="true" />
        {{ t('sa.cond.add') }}
      </AppButton>
      <span v-if="rows.length >= MAX_CONDITIONS" class="ml-2 text-xs text-muted">{{ tp('sa.cond.max', { max: String(MAX_CONDITIONS) }) }}</span>
    </div>
  </fieldset>
</template>

<script setup lang="ts">
import { PlusIcon, TrashIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import { INPUT_CLASS } from '../../lib/formClasses'
import { emptyRow, MAX_CONDITIONS, type ConditionRow } from '../../lib/oidcPolicies'
import { t, tp } from '../../i18n'

const props = defineProps<{ rows: ConditionRow[] }>()
const emit = defineEmits<{ 'update:rows': [rows: ConditionRow[]] }>()

const patch = (i: number, change: Partial<ConditionRow>): void =>
  emit('update:rows', props.rows.map((r, j) => (j === i ? { ...r, ...change } : r)))
const add = (): void => emit('update:rows', [...props.rows, emptyRow()])
const remove = (i: number): void => emit('update:rows', props.rows.filter((_, j) => j !== i))
</script>
