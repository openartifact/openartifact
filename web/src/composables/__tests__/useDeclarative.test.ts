// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, afterEach } from 'bun:test'
import { nextTick } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useDeclarative } from '../useDeclarative'

afterEach(() => {
  ;(globalThis.fetch as unknown as { mockRestore?: () => void }).mockRestore?.()
})

describe('useDeclarative', () => {
  it('drops a diff that answers after the manifest changed', async () => {
    setActivePinia(createPinia())
    let resolve: (r: Response) => void = () => {}
    spyOn(globalThis, 'fetch').mockImplementation(() => new Promise<Response>((r) => (resolve = r)))
    const d = useDeclarative(() => {})
    d.manifest.value = 'kind: Repository'
    await nextTick()
    const pending = d.review()
    d.manifest.value = 'kind: Project'
    await nextTick()
    resolve(
      new Response(JSON.stringify({ changes: true, items: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )
    expect(await pending).toBe(false)
    expect(d.plan.value).toBeNull()
    expect(d.applicable.value).toBe(false)
  })
})
