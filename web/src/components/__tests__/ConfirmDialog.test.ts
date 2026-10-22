// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect, afterEach } from 'bun:test'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import ConfirmDialog from '../ConfirmDialog.vue'

let wrapper: VueWrapper | undefined

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
})

function buttons(): HTMLButtonElement[] {
  return Array.from(document.body.querySelectorAll<HTMLButtonElement>('[role="dialog"] button'))
}

describe('ConfirmDialog', () => {
  it('renders a labelled modal dialog and emits confirm', async () => {
    wrapper = mount(ConfirmDialog, {
      props: { open: true, title: 'Delete it?', description: 'Gone for good', confirmLabel: 'Delete' },
      attachTo: document.body,
    })
    await flushPromises()
    const dialog = document.body.querySelector('[role="dialog"]')
    expect(dialog).not.toBeNull()
    expect(dialog?.textContent).toContain('Delete it?')
    expect(dialog?.textContent).toContain('Gone for good')
    buttons().find((b) => b.textContent?.includes('Delete'))?.click()
    expect(wrapper.emitted('confirm')).toHaveLength(1)
  })

  it('closes on Escape', async () => {
    wrapper = mount(ConfirmDialog, { props: { open: true, title: 'Delete it?' }, attachTo: document.body })
    await flushPromises()
    const dialog = document.body.querySelector('[role="dialog"]') as HTMLElement
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(wrapper.emitted('close')).toBeTruthy()
  })

  it('ignores Escape and Cancel while busy', async () => {
    wrapper = mount(ConfirmDialog, { props: { open: true, title: 'Delete it?', busy: true }, attachTo: document.body })
    await flushPromises()
    const dialog = document.body.querySelector('[role="dialog"]') as HTMLElement
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(wrapper.emitted('close')).toBeUndefined()
  })
})
