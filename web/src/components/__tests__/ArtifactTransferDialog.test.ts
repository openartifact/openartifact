// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, afterEach } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { setTransport } from '../../api/client'
import ArtifactTransferDialog from '../artifact/ArtifactTransferDialog.vue'

let wrapper: VueWrapper | undefined

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  setTransport()
})

const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

interface Call {
  path: string
  body: Record<string, unknown> | null
}

function install(reply: (call: Call) => Response): Call[] {
  const calls: Call[] = []
  setTransport((path, init) => {
    const call = { path, body: typeof init.body === 'string' ? (JSON.parse(init.body) as Record<string, unknown>) : null }
    calls.push(call)
    return Promise.resolve(reply(call))
  })
  return calls
}

const REPOS = { repositories: [{ name: 'src' }, { name: 'releases' }, { name: 'staging' }], total: 3 }

async function open(mode: 'copy' | 'move'): Promise<void> {
  wrapper = mount(ArtifactTransferDialog, {
    props: { open: true, mode, repo: 'src', format: 'maven', isAdmin: true, label: 'core 1.4.0', items: [{ package: 'core', version: '1.4.0' }] },
    attachTo: document.body,
  })
  await flushPromises()
}

const dialog = (): HTMLElement => document.body.querySelector('[role="dialog"]') as HTMLElement

async function choose(name: string): Promise<void> {
  const select = document.body.querySelector<HTMLSelectElement>('[data-testid="transfer-target"]')!
  select.value = name
  select.dispatchEvent(new Event('change'))
  await flushPromises()
}

describe('ArtifactTransferDialog', () => {
  it('lists the other hosted repositories, previews with a dry run and confirms', async () => {
    const calls = install((c) => (c.path.startsWith('/api/v1/repositories?') ? json(REPOS) : json({ operation: 'copy', copied: 2, existing: 1, replaced: 0, items: [] })))
    await open('copy')

    const options = Array.from(document.body.querySelectorAll('[data-testid="transfer-target"] option')).map((o) => o.textContent)
    expect(options).toEqual(['Choose a repository', 'releases', 'staging'])
    expect(calls[0].path).toContain('type=hosted')
    expect(calls[0].path).toContain('format=maven')

    await choose('releases')
    const dry = calls.find((c) => c.path === '/api/v1/repositories/src/copy')!
    expect(dry.body).toMatchObject({ target: 'releases', dry_run: true, items: [{ package: 'core', version: '1.4.0' }] })
    expect(dialog().textContent).toContain('2 files will be copied')
    expect(dialog().textContent).toContain('1 file is already in the target')

    document.body.querySelector<HTMLButtonElement>('[data-testid="transfer-confirm"]')!.click()
    await flushPromises()
    const real = calls.filter((c) => c.path === '/api/v1/repositories/src/copy')
    expect(real).toHaveLength(2)
    expect(real[1].body).toMatchObject({ dry_run: false })
    expect(wrapper!.emitted('done')?.[0]).toEqual(['releases'])
  })

  it('keeps confirm disabled and shows the server message when the preview fails', async () => {
    install((c) => (c.path.startsWith('/api/v1/repositories?') ? json(REPOS) : json({ error: 'the target repository holds different content', code: 'artifact_conflict' }, 409)))
    await open('move')
    await choose('staging')

    expect(dialog().textContent).toContain('the target repository holds different content')
    expect(document.body.querySelector<HTMLButtonElement>('[data-testid="transfer-confirm"]')!.disabled).toBe(true)
    expect(dialog().textContent).not.toContain('Nothing has been written yet')
  })

  it('says so when there is no target to offer', async () => {
    install(() => json({ repositories: [{ name: 'src' }], total: 1 }))
    await open('copy')

    expect(document.body.querySelector('[data-testid="transfer-no-targets"]')).not.toBeNull()
  })
})
