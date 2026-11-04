// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Component } from 'vue'
import { SparklesIcon, CloudIcon, PuzzlePieceIcon } from '@heroicons/vue/24/outline'
import { t } from '../i18n'

export interface ProviderMeta {
  label: string
  desc: string
  icon?: Component
  defaultUrl: string
  defaultModels: string[]
}

// Built lazily (inside a computed) so the descriptions follow the active language.
export function buildAIProviders(): Record<string, ProviderMeta> {
  return {
    openai: {
      label: 'OpenAI',
      desc: 'GPT-4o, GPT-4o-mini, o1',
      icon: SparklesIcon,
      defaultUrl: 'https://api.openai.com/v1',
      defaultModels: ['gpt-4o', 'gpt-4o-mini', 'o1-mini'],
    },
    azure: {
      label: 'Azure OpenAI',
      desc: t('adm.ai.provider.azure.desc'),
      icon: CloudIcon,
      defaultUrl: 'https://{resource}.openai.azure.com/openai/deployments/{deployment}',
      defaultModels: ['gpt-4o', 'gpt-35-turbo'],
    },
    ollama: {
      label: 'Ollama',
      desc: t('adm.ai.provider.ollama.desc'),
      defaultUrl: 'http://localhost:11434/v1',
      defaultModels: ['llama3.2', 'mistral', 'codellama'],
    },
    custom: {
      label: t('adm.ai.provider.custom.label'),
      desc: t('adm.ai.provider.custom.desc'),
      icon: PuzzlePieceIcon,
      defaultUrl: '',
      defaultModels: [],
    },
  }
}
