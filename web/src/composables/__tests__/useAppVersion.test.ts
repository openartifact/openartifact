// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, spyOn, type Mock } from 'bun:test'
import { ref } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { formatVersion, useApiReferenceEnabled, useAppVersion, useDocsUrl } from '../useAppVersion'
import { useDocsLink } from '../useDocsLink'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let fetchSpy: Mock<typeof fetch>

function serve(body: unknown, status = 200): void {
  fetchSpy = spyOn(globalThis, 'fetch').mockImplementation((async () => json(body, status)) as never)
}

// test/isolation.ts restores fetch and calls resetAppInfo() after every test.
describe('system info composables', () => {
  it('fetches /system/info once and shares version and docs_url', async () => {
    serve({ version: '0.9.0', status: 'ok', docs_url: 'https://docs.example.com/v0.9/' })
    const version = useAppVersion()
    const docs = useDocsUrl()
    useDocsUrl()
    await flushPromises()
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    expect(String(fetchSpy.mock.calls[0][0])).toBe('/api/v1/system/info')
    expect(version.value).toBe('0.9.0')
    expect(docs.value).toBe('https://docs.example.com/v0.9/')
  })

  it('has no docs url when the server omits it (docs links disabled)', async () => {
    serve({ version: '0.9.0', status: 'ok' })
    const docs = useDocsUrl()
    await flushPromises()
    expect(docs.value).toBeNull()
  })

  it('exposes whether the API reference is served', async () => {
    serve({ version: '0.9.0', api_reference_enabled: true })
    const on = useApiReferenceEnabled()
    await flushPromises()
    expect(on.value).toBe(true)
  })

  it('treats a missing api_reference_enabled as off', async () => {
    serve({ version: '0.9.0' })
    const on = useApiReferenceEnabled()
    await flushPromises()
    expect(on.value).toBe(false)
  })

  it('drops a docs url that is not http(s)', async () => {
    serve({ version: '0.9.0', docs_url: 'javascript:alert(1)//' })
    const docs = useDocsUrl()
    await flushPromises()
    expect(docs.value).toBeNull()
  })

  it('stays unknown on a failed request and retries on the next consumer', async () => {
    serve({ error: 'boom' }, 500)
    const docs = useDocsUrl()
    await flushPromises()
    expect(docs.value).toBeNull()
    fetchSpy.mockRestore()
    serve({ version: '0.9.0', docs_url: 'https://docs.example.com/latest/' })
    useDocsUrl()
    await flushPromises()
    expect(docs.value).toBe('https://docs.example.com/latest/')
  })

  it('useDocsLink resolves a route name against the shared docs url', async () => {
    serve({ version: 'dev', docs_url: 'https://docs.example.com/latest/' })
    const route = ref<string | undefined>('admin-sso')
    const link = useDocsLink(route)
    expect(link.value).toBeNull()
    await flushPromises()
    expect(link.value).toBe('https://docs.example.com/latest/security/sso')
    route.value = 'dashboard'
    expect(link.value).toBeNull()
    route.value = undefined
    expect(link.value).toBeNull()
  })
})

describe('formatVersion', () => {
  it('prefixes releases with v and keeps dev as is', () => {
    expect(formatVersion('1.2.3')).toBe('v1.2.3')
    expect(formatVersion('dev')).toBe('dev')
  })
})
