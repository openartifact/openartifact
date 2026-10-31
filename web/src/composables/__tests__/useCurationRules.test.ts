// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, spyOn, type Mock } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import { useCurationRules } from '../useCurationRules'
import type { CurationRepository, CurationRule } from '../../api/curation'
import { useToastStore } from '../../stores/toast'

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const rule = (over: Partial<CurationRule> = {}): CurationRule => ({
  id: 'r1',
  action: 'block',
  package_pattern: 'lodash',
  version_pattern: '',
  reason: '',
  created_by: 'ada',
  created_at: '2026-10-01T00:00:00Z',
  ...over,
})

interface Call {
  method: string
  path: string
  body: Record<string, unknown> | null
}

let calls: Call[]
let spy: Mock<typeof fetch>
let stored: CurationRule[]

/** A tiny server: the rules live in `stored`, every other call is answered by `special` or an empty 200. */
function serve(special: (c: Call) => Response | undefined = () => undefined): void {
  spy = spyOn(globalThis, 'fetch').mockImplementation((async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = init?.body
    const call: Call = {
      method: init?.method ?? 'GET',
      path: new URL(String(input), 'http://localhost').pathname,
      body: typeof raw === 'string' ? (JSON.parse(raw) as Record<string, unknown>) : null,
    }
    calls.push(call)
    const custom = special(call)
    if (custom) return custom
    if (call.path.endsWith('/curation-rules') && call.method === 'GET') return json({ rules: stored })
    return json({})
  }) as typeof fetch)
}

function panel(mode: 'off' | 'denylist' | 'allowlist' = 'denylist') {
  const adopted: CurationRepository[] = []
  const state = useCurationRules('npm-proxy', mode, (r) => adopted.push(r))
  return { state, adopted }
}

beforeEach(() => {
  setActivePinia(createPinia())
  calls = []
  stored = []
})
afterEach(() => spy?.mockRestore())

describe('useCurationRules: loading', () => {
  it('loads the rules of the repository', async () => {
    stored = [rule()]
    serve()
    const { state } = panel()
    await state.load()
    expect(calls[0]?.path).toBe('/api/v1/repositories/npm-proxy/curation-rules')
    expect(state.rules.value.map((r) => r.package_pattern)).toEqual(['lodash'])
    expect(state.loading.value).toBe(false)
  })

  it('keeps the failure for the table to show', async () => {
    serve(() => json({ error: 'boom' }, 500))
    const { state } = panel()
    await state.load()
    expect(state.loadError.value).toBe('boom')
    expect(state.loading.value).toBe(false)
  })

  it('warns when an allowlist has no allow rule, and only then', async () => {
    stored = [rule({ action: 'block' })]
    serve()
    const { state } = panel('allowlist')
    await state.load()
    expect(state.allowlistEmpty.value).toBe(true)
    stored = [rule({ action: 'allow' })]
    await state.load()
    expect(state.allowlistEmpty.value).toBe(false)
  })
})

describe('useCurationRules: mode', () => {
  it('patches the repository, adopts the answer and confirms with a toast', async () => {
    serve((c) => (c.method === 'PATCH' ? json({ name: 'npm-proxy', curation_mode: 'allowlist' }) : undefined))
    const { state, adopted } = panel('off')
    await state.changeMode('allowlist')
    const patch = calls.find((c) => c.method === 'PATCH')
    expect(patch?.path).toBe('/api/v1/repository/npm-proxy')
    expect(patch?.body).toEqual({ curation_mode: 'allowlist' })
    expect(state.mode.value).toBe('allowlist')
    expect(adopted).toHaveLength(1)
    expect(useToastStore().queue[0]?.title).toBe('Curation mode set to Allowlist')
  })

  it('goes back to the previous mode and says why when the server refuses', async () => {
    serve((c) => (c.method === 'PATCH' ? json({ error: 'not allowed' }, 403) : undefined))
    const { state, adopted } = panel('denylist')
    await state.changeMode('allowlist')
    expect(state.mode.value).toBe('denylist')
    expect(adopted).toHaveLength(0)
    const toast = useToastStore().queue[0]
    expect(toast?.level).toBe('error')
    expect(toast?.description).toBe('not allowed')
  })

  it('does not call the server for the mode it already has', async () => {
    serve()
    const { state } = panel('denylist')
    await state.changeMode('denylist')
    expect(calls).toHaveLength(0)
  })
})

describe('useCurationRules: rules', () => {
  it('creates a rule from the drawer form and reloads the list', async () => {
    serve()
    const { state } = panel('denylist')
    state.openAdd()
    state.form.value.packagePattern = ' left-pad '
    state.form.value.reason = 'abandoned'
    await state.save()
    const post = calls.find((c) => c.method === 'POST')
    expect(post?.path).toBe('/api/v1/repositories/npm-proxy/curation-rules')
    expect(post?.body).toEqual({ action: 'block', package_pattern: 'left-pad', version_pattern: '', reason: 'abandoned' })
    expect(state.drawerOpen.value).toBe(false)
    expect(calls.at(-1)?.method).toBe('GET')
  })

  it('does not call the server while the form is invalid', async () => {
    serve()
    const { state } = panel()
    state.openAdd()
    await state.save()
    expect(calls).toHaveLength(0)
    expect(state.drawerOpen.value).toBe(true)
  })

  it('updates the edited rule with every field of the form', async () => {
    serve()
    const { state } = panel()
    state.openEdit(rule({ id: 'r9', version_pattern: '1.*' }))
    state.form.value.versionPattern = ''
    await state.save()
    const patch = calls.find((c) => c.method === 'PATCH')
    expect(patch?.path).toBe('/api/v1/repositories/npm-proxy/curation-rules/r9')
    expect(patch?.body).toEqual({ action: 'block', package_pattern: 'lodash', version_pattern: '', reason: '' })
  })

  it('keeps the drawer open and shows the server message when a pattern is refused', async () => {
    serve((c) => (c.method === 'POST' ? json({ error: 'invalid package pattern' }, 400) : undefined))
    const { state } = panel()
    state.openAdd()
    state.form.value.packagePattern = '['
    await state.save()
    expect(state.formError.value).toBe('invalid package pattern')
    expect(state.drawerOpen.value).toBe(true)
    expect(state.saving.value).toBe(false)
  })

  it('deletes the confirmed rule and reloads', async () => {
    serve()
    const { state } = panel()
    state.deleteTarget.value = rule({ id: 'r4' })
    await state.confirmDelete()
    expect(calls.find((c) => c.method === 'DELETE')?.path).toBe('/api/v1/repositories/npm-proxy/curation-rules/r4')
    expect(state.deleteTarget.value).toBeNull()
  })

  it('keeps the confirmation open when the delete fails', async () => {
    serve((c) => (c.method === 'DELETE' ? json({ error: 'gone' }, 500) : undefined))
    const { state } = panel()
    state.deleteTarget.value = rule({ id: 'r4' })
    await state.confirmDelete()
    expect(state.deleteTarget.value?.id).toBe('r4')
    expect(useToastStore().queue[0]?.description).toBe('gone')
  })
})
