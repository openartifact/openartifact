// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import BackupPage from '../BackupPage.vue'

const mountPage = () => {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:rest(.*)*', component: { template: '<div />' } }] })
  return mount(BackupPage, { global: { plugins: [router] } })
}

const RUNNING = { backups: [{ id: 'b1', status: 'running', type: 'full', created_at: '2026-01-01T00:00:00Z' }] }
const DONE = { backups: [{ id: 'b1', status: 'completed', type: 'full', created_at: '2026-01-01T00:00:00Z' }] }

describe('BackupPage polling', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    document.body.innerHTML = ''
  })

  it('polls while a backup is running and stops once it finished', async () => {
    spyOn(globalThis, 'fetch').mockImplementation((async () => new Response(JSON.stringify(RUNNING), { status: 200 })) as never)
    const setIntervalSpy = spyOn(globalThis, 'setInterval')

    const wrapper = mountPage()
    await flushPromises()

    expect(setIntervalSpy).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('does not start a poll timer when the page unmounts while the first load is in flight', async () => {
    let finish: (res: Response) => void = () => {}
    spyOn(globalThis, 'fetch').mockImplementation((() => new Promise<Response>((resolve) => { finish = resolve })) as never)
    const setIntervalSpy = spyOn(globalThis, 'setInterval')

    const wrapper = mountPage()
    wrapper.unmount()
    finish(new Response(JSON.stringify(RUNNING), { status: 200 }))
    await flushPromises()

    expect(setIntervalSpy).not.toHaveBeenCalled()
  })

  it('does not poll for a list without active jobs', async () => {
    spyOn(globalThis, 'fetch').mockImplementation((async () => new Response(JSON.stringify(DONE), { status: 200 })) as never)
    const setIntervalSpy = spyOn(globalThis, 'setInterval')

    const wrapper = mountPage()
    await flushPromises()

    expect(setIntervalSpy).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
