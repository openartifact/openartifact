// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'

export interface AIConfig {
  enabled: boolean
  /** Analyse every upload automatically (off: analyses run only on request). */
  auto_analyze: boolean
  provider: string
  base_url: string
  /** Write-only: GET returns a mask; send the mask back to keep the stored key. */
  api_key: string
  /** Read-only: whether a key is stored. */
  has_api_key?: boolean
  model: string
  max_tokens: number
  timeout_secs: number
  system_prompt: string
}

export interface TestResult {
  ok: boolean
  models?: string[]
  error?: string
}

export function getAIConfig(): Promise<AIConfig> {
  return request<AIConfig>('/api/v1/admin/ai/config')
}

export function saveAIConfig(cfg: AIConfig): Promise<AIConfig> {
  return request<AIConfig>('/api/v1/admin/ai/config', {
    method: 'PUT',
    headers: JSON_HEADERS,
    body: JSON.stringify(cfg),
  })
}

export function testAIConfig(cfg: AIConfig): Promise<TestResult> {
  return request<TestResult>('/api/v1/admin/ai/test', {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(cfg),
  })
}
