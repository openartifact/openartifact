// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { defineComponent, h, type PropType, type VNode } from 'vue'
import { parseMiniMarkdown, type InlineNode } from '../lib/miniMarkdown'

function renderInline(nodes: InlineNode[]): (VNode | string)[] {
  return nodes.map((node) => {
    switch (node.type) {
      case 'text':
        return node.text
      case 'br':
        return h('br')
      case 'bold':
        return h('strong', { class: 'font-semibold text-foreground' }, renderInline(node.children))
      case 'italic':
        return h('em', renderInline(node.children))
      case 'code':
        return h('code', { class: 'rounded bg-surface-hover px-1 py-0.5 font-mono text-[0.9em]' }, node.text)
      case 'link':
        return h(
          'a',
          {
            href: node.href,
            target: '_blank',
            rel: 'noopener noreferrer',
            class: 'text-primary underline underline-offset-2 hover:opacity-80',
          },
          renderInline(node.children),
        )
    }
  })
}

/**
 * Renders the login-message markdown subset (see lib/miniMarkdown.ts) as
 * elements and text nodes: there is no v-html, so markup in the source is
 * shown literally.
 */
export default defineComponent({
  name: 'MiniMarkdown',
  props: { source: { type: String as PropType<string>, default: '' } },
  setup(props) {
    return () => {
      const paragraphs = parseMiniMarkdown(props.source)
      return h(
        'div',
        { class: 'space-y-2 break-words' },
        paragraphs.map((p) => h('p', renderInline(p.children))),
      )
    }
  },
})
