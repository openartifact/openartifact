// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, beforeEach, afterEach, mock, spyOn } from 'bun:test'
import { mount, flushPromises } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'
import CommandPalette from '../CommandPalette.vue'
import { setActivePinia, createPinia } from 'pinia'
import { useUIStore } from '../../stores/ui'
import { registerCommand, unregisterCommand, listCommands, type CommandEntry } from '../../composables/useCommands'

// We test the small surface this skill controls: store toggles and the
// command registry. The full Headless UI Dialog mounted via @vue/test-utils
// requires DOM features (transitions, portals) that are awkward to mock
// without dragging in @testing-library/vue, which is not part of the
// dependency baseline.

describe('CommandPalette wiring', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  // The command registry is module-level state shared by every test file.
  afterEach(() => {
    unregisterCommand('test.cmd')
    unregisterCommand('test.run')
  })

  it('UI store opens and closes the palette', () => {
    const ui = useUIStore()
    expect(ui.commandPaletteOpen).toBe(false)
    ui.openCommandPalette()
    expect(ui.commandPaletteOpen).toBe(true)
    ui.closeCommandPalette()
    expect(ui.commandPaletteOpen).toBe(false)
  })

  it('registerCommand returns an unregister function', () => {
    const run = mock()
    const off = registerCommand({
      id: 'test.cmd',
      title: 'Test command',
      group: 'commands',
      run,
    })
    expect(listCommands().some((c) => c.id === 'test.cmd')).toBe(true)
    off()
    expect(listCommands().some((c) => c.id === 'test.cmd')).toBe(false)
  })

  it('command run is invoked when called directly', async () => {
    const run = mock()
    registerCommand({ id: 'test.run', title: 'Run thing', group: 'commands', run })
    const cmd = listCommands().find((c) => c.id === 'test.run')!
    await cmd.run()
    expect(run).toHaveBeenCalled()
  })
})

describe('CommandPalette typing', () => {
  const RENDER_LIMIT = 200

  afterEach(() => {
    unregisterCommand('test.render-guard')
    mock.restore()
  })

  it('stays render-stable when the query changes', async () => {
    setActivePinia(createPinia())
    const searchApi = await import('../../api/search')
    spyOn(searchApi, 'search').mockResolvedValue({ packages: [], repositories: [] })

    // `hint` is read once per option render; a render loop trips the guard
    // instead of hanging the test runner.
    let renders = 0
    registerCommand({
      id: 'test.render-guard',
      title: 'Ui render guard',
      group: 'commands',
      get hint() {
        renders++
        if (renders > RENDER_LIMIT) throw new Error('render loop detected')
        return 'guard'
      },
      run: () => {},
    } as CommandEntry)

    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/', component: { template: '<div/>' } }],
    })
    const ui = useUIStore()
    ui.openCommandPalette()
    const wrapper = mount(CommandPalette, {
      global: { plugins: [router] },
      attachTo: document.body,
    })
    await flushPromises()

    const input = document.body.querySelector('input') as HTMLInputElement
    expect(input).not.toBeNull()
    input.value = 'u'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await flushPromises()
    await flushPromises()

    expect(renders).toBeLessThan(RENDER_LIMIT)
    expect(document.body.textContent).toContain('Ui render guard')
    wrapper.unmount()
  })
})
