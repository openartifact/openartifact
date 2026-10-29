// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import FindingAIDetail from '../FindingAIDetail.vue'
import type { SecurityFinding } from '../../../api/security'
import { t } from '../../../i18n'

const FINDING = { id: 'f1', type: 'ai', repository: 'libs', artifact_id: 'a1' } as SecurityFinding

const json = (body: unknown, status: number): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

describe('FindingAIDetail', () => {
  it('words a 501 feature_unavailable as "not part of this build", not as a failed load', async () => {
    spyOn(globalThis, 'fetch').mockImplementation((async () =>
      json({ error: 'AI analysis is not compiled into this build (built with -tags noai)', code: 'feature_unavailable' }, 501)) as never)
    const w = mount(FindingAIDetail, { props: { finding: FINDING, canAct: true }, global: { plugins: [createPinia()] } })
    await flushPromises()
    expect(w.get('[role="alert"]').text()).toBe(t('security.ai.unavailable'))
    w.unmount()
  })

  it('still shows the server message for any other failure', async () => {
    spyOn(globalThis, 'fetch').mockImplementation((async () => json({ error: 'database down', code: 'internal_error' }, 500)) as never)
    const w = mount(FindingAIDetail, { props: { finding: FINDING, canAct: true }, global: { plugins: [createPinia()] } })
    await flushPromises()
    expect(w.get('[role="alert"]').text()).toBe('database down')
    w.unmount()
  })
})
