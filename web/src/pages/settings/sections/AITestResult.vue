<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <Transition
    enter-active-class="transition-all duration-300 ease-out"
    enter-from-class="opacity-0 -translate-y-2"
    enter-to-class="opacity-100 translate-y-0"
    leave-active-class="transition-all duration-200 ease-in"
    leave-from-class="opacity-100 translate-y-0"
    leave-to-class="opacity-0 -translate-y-1"
  >
    <div
      v-if="result !== null"
      class="rounded-xl border p-4"
      role="status"
      :class="result.ok ? 'border-success/30 bg-success-soft' : 'border-danger/30 bg-danger-soft'"
    >
      <div class="flex items-start gap-3">
        <div class="w-8 h-8 rounded-lg flex items-center justify-center shrink-0 mt-0.5" :class="result.ok ? 'bg-success/20' : 'bg-danger/20'">
          <CheckCircleIcon v-if="result.ok" class="w-4 h-4 text-success" aria-hidden="true" />
          <ExclamationCircleIcon v-else class="w-4 h-4 text-danger" aria-hidden="true" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-semibold" :class="result.ok ? 'text-success' : 'text-danger'">
            {{ result.ok ? t('adm.ai.testOk') : t('adm.ai.testFailed') }}
          </p>
          <p v-if="result.error" class="text-xs mt-0.5 font-mono break-all" :class="result.ok ? 'text-success/80' : 'text-danger/80'">
            {{ result.error }}
          </p>
          <div v-if="result.ok && result.models && result.models.length > 0" class="flex flex-wrap gap-1.5 mt-2">
            <button
              v-for="m in result.models"
              :key="m"
              type="button"
              class="inline-flex items-center px-2 py-0.5 rounded-md bg-success/15 text-success text-caption font-mono font-medium border border-success/20 hover:bg-success/25 transition-colors"
              :title="`${t('adm.ai.useModel')} ${m}`"
              @click="emit('use-model', m)"
            >
              {{ m }}
            </button>
          </div>
          <p v-if="result.ok && result.models && result.models.length > 0" class="text-caption text-success/70 mt-1.5">
            {{ t('adm.ai.pickModelHint') }}
          </p>
        </div>
        <button
          type="button"
          :aria-label="t('common.close')"
          class="shrink-0 p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover/60 transition-colors"
          @click="emit('close')"
        >
          <XMarkIcon class="w-4 h-4" aria-hidden="true" />
        </button>
      </div>
    </div>
  </Transition>
</template>

<script setup lang="ts">
import { CheckCircleIcon, ExclamationCircleIcon, XMarkIcon } from '@heroicons/vue/24/outline'
import type { TestResult } from '../../../api/adminAi'
import { t } from '../../../i18n'

defineProps<{ result: TestResult | null }>()
const emit = defineEmits<{ (e: 'use-model', model: string): void; (e: 'close'): void }>()
</script>
