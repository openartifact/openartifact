// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import Drawer from '../Drawer.vue'

describe('Drawer', () => {
  it('starts in the field a form marks with data-autofocus', async () => {
    const w = mount(Drawer, {
      props: { open: true, title: 'New thing' },
      slots: { default: '<input id="first" /><input id="name" data-autofocus />' },
      attachTo: document.body,
    })
    await flushPromises()
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(document.activeElement?.id).toBe('name')
    w.unmount()
  })
})
