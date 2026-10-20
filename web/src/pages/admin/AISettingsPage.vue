<!--
SPDX-FileCopyrightText: The OpenArtifact Authors

SPDX-License-Identifier: AGPL-3.0-or-later
-->

<template>
  <div class="space-y-5">
    <!-- Page Header -->
    <div class="flex items-start justify-between flex-wrap gap-3">
      <div>
        <div class="flex items-center gap-2 text-[11px] uppercase tracking-[0.14em] text-muted font-mono">
          <span>{{ t('nav.admin') }}</span>
          <ChevronRightIcon class="w-3 h-3" aria-hidden="true" />
          <span class="text-foreground/70">{{ t('adm.ai.title') }}</span>
        </div>
        <h1 class="mt-1 text-2xl font-bold tracking-tight">{{ t('adm.ai.title') }}</h1>
        <p class="mt-1 text-sm text-muted">{{ t('adm.ai.description') }}</p>
      </div>
      <div class="flex items-center gap-2 shrink-0">
        <button
          type="button"
          :disabled="testing || loading"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium border border-border bg-surface text-foreground hover:bg-surface-hover transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          @click="testConnection"
        >
          <ArrowPathIcon class="w-4 h-4" :class="{ 'animate-spin': testing }" aria-hidden="true" />
          {{ t('adm.ai.testConnection') }}
        </button>
        <button
          type="button"
          :disabled="saving || loading"
          class="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-lg text-sm font-medium bg-primary text-primary-contrast hover:opacity-90 transition-opacity disabled:opacity-50 disabled:cursor-not-allowed"
          @click="save"
        >
          <ArrowPathIcon v-if="saving" class="w-4 h-4 animate-spin" aria-hidden="true" />
          <CheckIcon v-else class="w-4 h-4" aria-hidden="true" />
          {{ saving ? t('common.saving') : t('adm.ai.saveChanges') }}
        </button>
      </div>
    </div>

    <!-- Loading state -->
    <div v-if="loading" class="bg-surface rounded-xl border border-border p-12 flex flex-col items-center gap-3 text-muted" role="status">
      <ArrowPathIcon class="w-6 h-6 animate-spin" aria-hidden="true" />
      <span class="text-sm">{{ t('adm.ai.loading') }}</span>
    </div>

    <div v-else-if="loadError" class="bg-surface rounded-xl border border-border p-8 text-center text-danger" role="alert">
      {{ loadError }}
    </div>

    <!-- Form body -->
    <template v-else>
      <!-- Enable Toggle Banner -->
      <div class="bg-surface rounded-xl border border-border p-5 flex items-start gap-4">
        <button
          type="button"
          role="switch"
          aria-labelledby="ai-enable-label"
          :aria-checked="form.enabled"
          class="relative inline-flex h-6 w-11 shrink-0 rounded-full transition-colors duration-200 mt-0.5 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
          :class="form.enabled ? 'bg-primary' : 'bg-border'"
          @click="form.enabled = !form.enabled"
        >
          <span
            class="inline-block h-5 w-5 rounded-full bg-white shadow-sm transition-transform duration-200 mt-0.5"
            :class="form.enabled ? 'translate-x-5' : 'translate-x-0.5'"
          />
        </button>
        <div class="min-w-0">
          <p id="ai-enable-label" class="text-sm font-semibold text-foreground leading-tight">{{ t('adm.ai.enable') }}</p>
          <p class="text-[12px] text-muted mt-0.5 leading-relaxed">{{ t('adm.ai.enableHint') }}</p>
        </div>
        <span
          class="shrink-0 inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-semibold"
          :class="form.enabled ? 'bg-success-soft text-success' : 'bg-surface-hover text-muted'"
        >
          <span class="w-1.5 h-1.5 rounded-full" :class="form.enabled ? 'bg-success' : 'bg-muted'" aria-hidden="true" />
          {{ form.enabled ? t('adm.sso.enabled') : t('adm.sso.disabled') }}
        </span>
      </div>

      <!-- Form content (fades when disabled) -->
      <div class="space-y-4 transition-opacity duration-200" :class="{ 'opacity-40 pointer-events-none': !form.enabled }" :aria-disabled="!form.enabled">
        <!-- Provider Picker -->
        <div class="bg-surface rounded-xl border border-border p-5 space-y-3">
          <div>
            <h3 class="text-sm font-semibold text-foreground">{{ t('adm.ai.provider') }}</h3>
            <p class="text-[11px] text-muted mt-0.5">{{ t('adm.ai.providerHint') }}</p>
          </div>
          <div class="grid grid-cols-2 gap-2" role="radiogroup" :aria-label="t('adm.ai.provider')">
            <button
              v-for="(meta, key) in PROVIDERS"
              :key="key"
              type="button"
              role="radio"
              :aria-checked="form.provider === key"
              :tabindex="form.enabled ? 0 : -1"
              class="relative text-left p-3 rounded-lg border overflow-hidden transition-all focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              :class="form.provider === key
                ? 'border-primary bg-primary-soft/40 ring-1 ring-primary/30'
                : 'border-border bg-surface hover:bg-surface-hover cursor-pointer'"
              @click="selectProvider(key)"
            >
              <span class="absolute left-0 top-0 bottom-0 w-[3px] rounded-l-lg" :style="{ background: meta.color }" />
              <div class="pl-3 flex items-start gap-2.5">
                <div class="w-7 h-7 rounded-md flex items-center justify-center shrink-0 mt-0.5" :style="{ background: meta.color + '20' }">
                  <svg
                    v-if="aiBrandIcon(String(key))"
                    class="w-4 h-4"
                    :style="{ color: meta.color }"
                    aria-hidden="true"
                    :viewBox="aiBrandIcon(String(key))!.viewBox"
                    fill="currentColor"
                  >
                    <path :d="aiBrandIcon(String(key))!.path" />
                  </svg>
                  <component v-else :is="meta.icon" class="w-4 h-4" :style="{ color: meta.color }" aria-hidden="true" />
                </div>
                <div class="min-w-0">
                  <p class="text-[12px] font-semibold text-foreground leading-tight">{{ meta.label }}</p>
                  <p class="text-[11px] text-muted leading-snug mt-0.5">{{ meta.desc }}</p>
                </div>
              </div>
            </button>
          </div>
        </div>

        <!-- Configuration Fields -->
        <div class="bg-surface rounded-xl border border-border p-5 space-y-4">
          <div>
            <h3 class="text-sm font-semibold text-foreground">{{ t('adm.ai.configuration') }}</h3>
            <p class="text-[11px] text-muted mt-0.5">{{ t('adm.ai.configurationHint') }}</p>
          </div>

          <div class="grid grid-cols-2 gap-x-4 gap-y-4">
            <div class="col-span-2 sm:col-span-1">
              <label for="ai-base-url" class="block text-xs font-semibold text-foreground mb-1.5">
                {{ t('adm.ai.baseUrl') }} <span class="text-danger ml-0.5">*</span>
              </label>
              <input
                id="ai-base-url"
                v-model="form.base_url"
                type="text"
                placeholder="https://api.openai.com/v1"
                class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
              />
              <p class="text-[11px] text-muted mt-1">{{ t('adm.ai.baseUrlHint') }}</p>
            </div>

            <div class="col-span-2 sm:col-span-1">
              <label for="ai-api-key" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.ai.apiKey') }}</label>
              <div class="relative">
                <input
                  id="ai-api-key"
                  v-model="form.api_key"
                  :type="apiKeyRevealed ? 'text' : 'password'"
                  autocomplete="off"
                  placeholder="••••••••"
                  class="w-full px-3 py-2 pr-14 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
                />
                <button
                  type="button"
                  :aria-label="apiKeyRevealed ? t('adm.ai.hideKey') : t('adm.ai.showKey')"
                  class="absolute right-2.5 top-1/2 -translate-y-1/2 text-[11px] font-medium text-muted hover:text-foreground transition-colors"
                  @click="apiKeyRevealed = !apiKeyRevealed"
                >
                  {{ apiKeyRevealed ? t('adm.ai.hide') : t('adm.ai.show') }}
                </button>
              </div>
              <p class="text-[11px] text-muted mt-1">{{ t('adm.ai.apiKeyHint') }}</p>
            </div>

            <div class="col-span-2 sm:col-span-1">
              <label for="ai-model" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.ai.model') }}</label>
              <div class="flex gap-2">
                <div class="relative flex-1 min-w-0">
                  <input
                    id="ai-model"
                    v-model="form.model"
                    type="text"
                    list="model-suggestions"
                    :placeholder="PROVIDERS[form.provider]?.defaultModels?.[0] ?? 'gpt-4o'"
                    class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
                  />
                  <datalist id="model-suggestions">
                    <option v-for="m in modelSuggestions" :key="m" :value="m" />
                  </datalist>
                </div>
                <button
                  type="button"
                  :disabled="loadingModels"
                  class="shrink-0 inline-flex items-center gap-1.5 px-3 py-2 rounded-lg text-sm font-medium border border-border bg-surface text-foreground hover:bg-surface-hover transition-colors disabled:opacity-40 disabled:cursor-not-allowed whitespace-nowrap"
                  :title="t('adm.ai.loadModelsHint')"
                  @click="loadModels"
                >
                  <ArrowPathIcon class="w-3.5 h-3.5" :class="{ 'animate-spin': loadingModels }" aria-hidden="true" />
                  {{ t('adm.ai.load') }}
                </button>
              </div>
              <p v-if="availableModels.length > 0" class="text-[11px] text-muted mt-1">
                {{ availableModels.length }} {{ t('adm.ai.modelsAvailable') }}
              </p>
              <p v-else class="text-[11px] text-muted mt-1">{{ t('adm.ai.modelHint') }}</p>
            </div>

            <div class="col-span-2 sm:col-span-1">
              <label for="ai-max-tokens" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.ai.maxTokens') }}</label>
              <input
                id="ai-max-tokens"
                v-model.number="form.max_tokens"
                type="number"
                min="128"
                max="128000"
                step="128"
                class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
              />
              <p class="text-[11px] text-muted mt-1">{{ t('adm.ai.maxTokensHint') }}</p>
            </div>

            <div class="col-span-2 sm:col-span-1">
              <label for="ai-timeout" class="block text-xs font-semibold text-foreground mb-1.5">{{ t('adm.ai.timeout') }}</label>
              <input
                id="ai-timeout"
                v-model.number="form.timeout_secs"
                type="number"
                min="5"
                max="300"
                step="5"
                class="w-full px-3 py-2 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted/50 focus:outline-none focus:ring-2 focus:ring-primary transition-colors"
              />
              <p class="text-[11px] text-muted mt-1">{{ t('adm.ai.timeoutHint') }}</p>
            </div>
          </div>
        </div>

        <!-- System Prompt -->
        <div class="bg-surface rounded-xl border border-border p-5 space-y-3">
          <div class="flex items-start justify-between gap-3">
            <div>
              <label for="ai-system-prompt" class="block text-sm font-semibold text-foreground">{{ t('adm.ai.systemPrompt') }}</label>
              <p class="text-[11px] text-muted mt-0.5">{{ t('adm.ai.systemPromptHint') }}</p>
            </div>
            <button
              v-if="form.system_prompt"
              type="button"
              class="shrink-0 text-[11px] font-medium text-muted hover:text-danger transition-colors"
              @click="form.system_prompt = ''"
            >
              {{ t('adm.ai.resetPrompt') }}
            </button>
          </div>
          <textarea
            id="ai-system-prompt"
            v-model="form.system_prompt"
            rows="6"
            :placeholder="t('adm.ai.systemPromptPlaceholder')"
            class="w-full px-3 py-2.5 rounded-lg border border-border bg-bg text-foreground text-sm font-mono placeholder:text-muted/40 focus:outline-none focus:ring-2 focus:ring-primary resize-none transition-colors leading-relaxed"
          />
        </div>
      </div>

      <!-- Test Result Panel -->
      <Transition
        enter-active-class="transition-all duration-300 ease-out"
        enter-from-class="opacity-0 -translate-y-2"
        enter-to-class="opacity-100 translate-y-0"
        leave-active-class="transition-all duration-200 ease-in"
        leave-from-class="opacity-100 translate-y-0"
        leave-to-class="opacity-0 -translate-y-1"
      >
        <div
          v-if="testResult !== null"
          class="rounded-xl border p-4"
          role="status"
          :class="testResult.ok ? 'border-success/30 bg-success-soft' : 'border-danger/30 bg-danger-soft'"
        >
          <div class="flex items-start gap-3">
            <div class="w-8 h-8 rounded-lg flex items-center justify-center shrink-0 mt-0.5" :class="testResult.ok ? 'bg-success/20' : 'bg-danger/20'">
              <CheckCircleIcon v-if="testResult.ok" class="w-4 h-4 text-success" aria-hidden="true" />
              <ExclamationCircleIcon v-else class="w-4 h-4 text-danger" aria-hidden="true" />
            </div>
            <div class="min-w-0 flex-1">
              <p class="text-sm font-semibold" :class="testResult.ok ? 'text-success' : 'text-danger'">
                {{ testResult.ok ? t('adm.ai.testOk') : t('adm.ai.testFailed') }}
              </p>
              <p v-if="testResult.error" class="text-[12px] mt-0.5 font-mono break-all" :class="testResult.ok ? 'text-success/80' : 'text-danger/80'">
                {{ testResult.error }}
              </p>
              <div v-if="testResult.ok && testResult.models && testResult.models.length > 0" class="flex flex-wrap gap-1.5 mt-2">
                <button
                  v-for="m in testResult.models"
                  :key="m"
                  type="button"
                  class="inline-flex items-center px-2 py-0.5 rounded-md bg-success/15 text-success text-[11px] font-mono font-medium border border-success/20 hover:bg-success/25 transition-colors"
                  :title="`${t('adm.ai.useModel')} ${m}`"
                  @click="form.model = m"
                >
                  {{ m }}
                </button>
              </div>
              <p v-if="testResult.ok && testResult.models && testResult.models.length > 0" class="text-[11px] text-success/70 mt-1.5">
                {{ t('adm.ai.pickModelHint') }}
              </p>
            </div>
            <button
              type="button"
              :aria-label="t('common.close')"
              class="shrink-0 p-1.5 rounded-md text-muted hover:text-foreground hover:bg-surface-hover/60 transition-colors"
              @click="testResult = null"
            >
              <XMarkIcon class="w-4 h-4" aria-hidden="true" />
            </button>
          </div>
        </div>
      </Transition>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import type { Component } from 'vue'
import {
  ChevronRightIcon,
  ArrowPathIcon,
  CheckIcon,
  CheckCircleIcon,
  ExclamationCircleIcon,
  XMarkIcon,
  SparklesIcon,
  CloudIcon,
  PuzzlePieceIcon,
} from '@heroicons/vue/24/outline'
import { useToastStore } from '../../stores/toast'
import { getAIConfig, saveAIConfig, testAIConfig } from '../../api/adminAi'
import type { AIConfig, TestResult } from '../../api/adminAi'
import { aiBrandIcon } from '../../lib/brandIcons'
import { t } from '../../i18n/en'

interface ProviderMeta {
  label: string
  desc: string
  color: string
  icon?: Component
  defaultUrl: string
  defaultModels: string[]
}

const PROVIDERS: Record<string, ProviderMeta> = {
  openai: {
    label: 'OpenAI',
    desc: 'GPT-4o, GPT-4o-mini, o1',
    color: '#10a37f',
    icon: SparklesIcon,
    defaultUrl: 'https://api.openai.com/v1',
    defaultModels: ['gpt-4o', 'gpt-4o-mini', 'o1-mini'],
  },
  azure: {
    label: 'Azure OpenAI',
    desc: 'Azure-hosted OpenAI models',
    color: '#0078d4',
    icon: CloudIcon,
    defaultUrl: 'https://{resource}.openai.azure.com/openai/deployments/{deployment}',
    defaultModels: ['gpt-4o', 'gpt-35-turbo'],
  },
  ollama: {
    label: 'Ollama',
    desc: 'Local models via Ollama',
    color: '#f97316',
    defaultUrl: 'http://localhost:11434/v1',
    defaultModels: ['llama3.2', 'mistral', 'codellama'],
  },
  custom: {
    label: 'Custom / OpenAI-compatible',
    desc: 'Any OpenAI-compatible endpoint',
    color: '#8b5cf6',
    icon: PuzzlePieceIcon,
    defaultUrl: '',
    defaultModels: [],
  },
}

const toasts = useToastStore()

const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const testing = ref(false)
const loadingModels = ref(false)
const apiKeyRevealed = ref(false)
const availableModels = ref<string[]>([])
const testResult = ref<TestResult | null>(null)

const form = reactive<AIConfig>({
  enabled: false,
  provider: 'openai',
  base_url: '',
  api_key: '',
  model: '',
  max_tokens: 2048,
  timeout_secs: 30,
  system_prompt: '',
})

const modelSuggestions = computed<string[]>(() => {
  const defaults = PROVIDERS[form.provider]?.defaultModels ?? []
  return [...new Set([...availableModels.value, ...defaults])]
})

function selectProvider(key: string): void {
  const prev = form.provider
  form.provider = key
  availableModels.value = []
  const prevDefault = PROVIDERS[prev]?.defaultUrl ?? ''
  const newDefault = PROVIDERS[key]?.defaultUrl ?? ''
  if (!form.base_url || form.base_url === prevDefault) {
    form.base_url = newDefault
  }
}

// The API key is write-only. GET returns a mask, and the backend treats the
// mask (or an empty key on test/models) as "use the stored key" while PUT with
// the mask leaves the stored key unchanged, so the mask is sent back as-is.
async function loadModels(): Promise<void> {
  loadingModels.value = true
  try {
    const result = await testAIConfig({ ...form })
    if (result.ok && result.models && result.models.length > 0) {
      availableModels.value = result.models
      toasts.success(`${t('adm.ai.loadedModels')} ${result.models.length}`)
    } else if (!result.ok) {
      toasts.error(result.error ?? t('adm.ai.loadModelsFailed'))
    } else {
      toasts.info(t('adm.ai.noModels'))
    }
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.ai.loadModelsFailed'))
  } finally {
    loadingModels.value = false
  }
}

async function testConnection(): Promise<void> {
  testing.value = true
  testResult.value = null
  try {
    const result = await testAIConfig({ ...form })
    testResult.value = result
    if (result.ok && result.models && result.models.length > 0) {
      availableModels.value = result.models
    }
  } catch (err) {
    testResult.value = {
      ok: false,
      error: err instanceof Error ? err.message : t('adm.ai.testFailed'),
    }
  } finally {
    testing.value = false
  }
}

async function save(): Promise<void> {
  if (saving.value) return
  saving.value = true
  try {
    await saveAIConfig({ ...form })
    toasts.success(t('adm.ai.saved'))
  } catch (err) {
    toasts.error(err instanceof Error ? err.message : t('adm.ai.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(async () => {
  try {
    const cfg = await getAIConfig()
    form.enabled = cfg.enabled
    form.provider = cfg.provider || 'openai'
    form.base_url = cfg.base_url || PROVIDERS[form.provider]?.defaultUrl || ''
    form.api_key = cfg.api_key || ''
    form.model = cfg.model || ''
    form.max_tokens = cfg.max_tokens || 2048
    form.timeout_secs = cfg.timeout_secs || 30
    form.system_prompt = cfg.system_prompt || ''
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : t('adm.ai.loadFailed')
    toasts.error(loadError.value)
  } finally {
    loading.value = false
  }
})
</script>
