<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <section class="mb-8 rounded-lg border border-border bg-surface" aria-labelledby="dash-checklist" data-testid="checklist">
    <div class="flex flex-wrap items-center gap-2 border-b border-border px-4 py-3">
      <h2 id="dash-checklist" class="text-base font-semibold text-foreground">{{ t('dashboard.checklist.title') }}</h2>
      <span class="text-xs text-muted">{{ tp('dashboard.checklist.progress', { done: String(done), total: String(steps.length) }) }}</span>
      <span class="flex-1" />
      <AppButton variant="ghost" size="sm" class="max-md:min-h-11" @click="emit('dismiss')">{{ t('dashboard.checklist.dismiss') }}</AppButton>
    </div>
    <ol class="divide-y divide-border" role="list">
      <li v-for="step in steps" :key="step.id" class="flex items-center gap-3 px-4 py-3 max-md:min-h-11">
        <CheckCircleIcon v-if="step.done" class="h-5 w-5 shrink-0 text-success" aria-hidden="true" />
        <ChevronRightIcon v-else class="h-5 w-5 shrink-0 text-muted" aria-hidden="true" />
        <div class="min-w-0 flex-1">
          <div class="text-sm font-medium text-foreground">{{ tp(key(step, 'title'), step.params) }}</div>
          <div class="text-xs text-muted">
            <span class="sr-only">{{ step.done ? t('dashboard.checklist.done') : '' }} </span>{{ tp(key(step, step.done ? 'done' : 'todo'), step.params) }}
          </div>
        </div>
        <template v-if="!step.done">
          <a v-if="step.to.startsWith('#')" :href="step.to" class="shrink-0 text-xs text-primary hover:underline max-md:flex max-md:min-h-11 max-md:items-center">{{ tp(key(step, 'action'), step.params) }}</a>
          <router-link v-else :to="step.to" class="shrink-0 text-xs text-primary hover:underline max-md:flex max-md:min-h-11 max-md:items-center">{{ tp(key(step, 'action'), step.params) }}</router-link>
        </template>
      </li>
    </ol>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircleIcon, ChevronRightIcon } from '@heroicons/vue/20/solid'
import AppButton from '../AppButton.vue'
import { t, tp, type TranslationKey } from '../../i18n'
import type { ChecklistStep } from '../../lib/checklist'

const props = defineProps<{ steps: ChecklistStep[] }>()
const emit = defineEmits<{ dismiss: [] }>()

const done = computed(() => props.steps.filter((s) => s.done).length)
const key = (step: ChecklistStep, part: string): TranslationKey => `dashboard.step.${step.id}.${part}` as TranslationKey
</script>
