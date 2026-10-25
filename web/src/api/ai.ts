// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { request, JSON_HEADERS } from './client'

export interface AIAnalysis {
  id: string
  repository_id: string
  artifact_id: string
  artifact_format: string
  artifact_name: string
  artifact_version: string
  summary: string
  tags: string[]
  license_status: 'SAFE' | 'REVIEW_REQUIRED' | 'UNKNOWN'
  license_name: string
  security_notes: string
  quarantine_recommended: boolean
  status: 'pending' | 'processing' | 'completed' | 'failed'
  error_message: string
  model_used: string
  prompt_tokens: number
  completion_tokens: number
  created_at: string
  updated_at: string
}

interface ListAIAnalysesResult {
  analyses: AIAnalysis[]
  total: number
}

/** The server clamps `limit` to 100 (default 20). */
export function listAIAnalyses(
  repoName: string,
  params?: { status?: string; limit?: number; offset?: number },
): Promise<ListAIAnalysesResult> {
  const qs = new URLSearchParams()
  if (params?.status) qs.set('status', params.status)
  if (params?.limit != null) qs.set('limit', String(params.limit))
  if (params?.offset != null) qs.set('offset', String(params.offset))
  const query = qs.toString() ? `?${qs}` : ''
  return request<ListAIAnalysesResult>(
    `/api/v1/repositories/${encodeURIComponent(repoName)}/ai-analysis${query}`,
  )
}

interface TriggerAIAnalysisOptions {
  manifest_text?: string
  artifact_format?: string
  artifact_name?: string
  artifact_version?: string
}

/** Queues an analysis (202); the result appears in the list once the worker finishes. */
export function triggerAIAnalysis(
  repoName: string,
  artifactID: string,
  options: TriggerAIAnalysisOptions = {},
): Promise<{ status: string; message: string }> {
  return request<{ status: string; message: string }>(
    `/api/v1/repositories/${encodeURIComponent(repoName)}/ai-analysis/${encodeURIComponent(artifactID)}/trigger`,
    {
      method: 'POST',
      headers: JSON_HEADERS,
      body: JSON.stringify(options),
    },
  )
}
