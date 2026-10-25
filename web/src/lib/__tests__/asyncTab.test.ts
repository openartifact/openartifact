// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { asyncTab } from '../asyncTab'

describe('asyncTab', () => {
  it('shows the skeleton while the chunk loads, then the tab with its props', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => (release = resolve))
    const Panel = defineComponent({
      props: { label: { type: String, required: true } },
      setup: (props) => () => h('p', { 'data-testid': 'panel' }, props.label),
    })
    const Lazy = asyncTab(async () => {
      await gate
      // A real dynamic import resolves to a module namespace object; Vue unwraps `default` only for those.
      return Object.defineProperty({ default: Panel }, Symbol.toStringTag, { value: 'Module' })
    })

    // Mounted under a parent, as the pages use it: a root async component does not re-render in test-utils.
    const w = mount(defineComponent({ render: () => h('div', [h(Lazy, { label: 'loaded' })]) }))
    expect(w.find('[data-testid="tab-skeleton"]').exists()).toBe(true)
    // Props meant for the tab must not leak onto the skeleton element.
    expect(w.get('[data-testid="tab-skeleton"]').attributes('label')).toBeUndefined()
    expect(w.find('[data-testid="panel"]').exists()).toBe(false)

    release()
    await flushPromises()
    await flushPromises()
    expect(w.find('[data-testid="tab-skeleton"]').exists()).toBe(false)
    expect(w.get('[data-testid="panel"]').text()).toBe('loaded')
  })
})
