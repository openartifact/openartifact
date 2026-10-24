// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { mount } from '@vue/test-utils'
import MiniMarkdown from '../../components/MiniMarkdown'
import { MAX_SOURCE_LENGTH, parseMiniMarkdown, type InlineNode } from '../miniMarkdown'

function inline(src: string): InlineNode[] {
  return parseMiniMarkdown(src)[0]?.children ?? []
}

function hrefs(nodes: InlineNode[]): string[] {
  return nodes.flatMap((n) => (n.type === 'link' ? [n.href, ...hrefs(n.children)] : 'children' in n ? hrefs(n.children) : []))
}

describe('parseMiniMarkdown', () => {
  it('splits paragraphs on blank lines and keeps single line breaks', () => {
    const out = parseMiniMarkdown('one\ntwo\n\n\nthree')
    expect(out).toHaveLength(2)
    expect(out[0]!.children).toEqual([{ type: 'text', text: 'one' }, { type: 'br' }, { type: 'text', text: 'two' }])
    expect(out[1]!.children).toEqual([{ type: 'text', text: 'three' }])
  })

  it('gives nothing for empty input', () => {
    expect(parseMiniMarkdown('')).toEqual([])
    expect(parseMiniMarkdown(' \n\n \n')).toEqual([])
  })

  it('handles bold, italic and code, including nesting', () => {
    expect(inline('a **b *c* d** `e*f*`')).toEqual([
      { type: 'text', text: 'a ' },
      { type: 'bold', children: [{ type: 'text', text: 'b ' }, { type: 'italic', children: [{ type: 'text', text: 'c' }] }, { type: 'text', text: ' d' }] },
      { type: 'text', text: ' ' },
      { type: 'code', text: 'e*f*' },
    ])
  })

  it('links only http, https and mailto', () => {
    expect(hrefs(inline('[a](https://example.com) [b](http://example.com/x) [c](mailto:ops@example.com)'))).toEqual([
      'https://example.com',
      'http://example.com/x',
      'mailto:ops@example.com',
    ])
  })

  it('shows hostile links as typed instead of linking them', () => {
    for (const src of [
      '[x](javascript:alert(1))',
      '[x](JaVaScRiPt:alert(1))',
      '[x](  javascript:alert(1))',
      '[x](data:text/html;base64,PHNjcmlwdD4=)',
      '[x](vbscript:x)',
      '[x](//evil.example)',
      '[x](/relative)',
      '[x](https://a.test/ b)',
    ]) {
      const nodes = inline(src)
      expect(hrefs(nodes)).toEqual([])
      expect(nodes.every((n) => n.type === 'text')).toBe(true)
    }
  })

  it('does not nest links', () => {
    const nodes = inline('[a [b](https://inner.test)](https://outer.test)')
    expect(hrefs(nodes).length).toBeLessThanOrEqual(1)
  })

  it('keeps html as literal text', () => {
    const nodes = inline('<script>alert(1)</script> <img src=x onerror=alert(1)> <b>x</b>')
    expect(nodes).toEqual([{ type: 'text', text: '<script>alert(1)</script> <img src=x onerror=alert(1)> <b>x</b>' }])
  })

  it('shows unbalanced markers as typed', () => {
    expect(inline('**oops')).toEqual([{ type: 'text', text: '**oops' }])
    expect(inline('a * b')).toEqual([{ type: 'text', text: 'a * b' }])
    expect(inline('`open')).toEqual([{ type: 'text', text: '`open' }])
    expect(inline('[a](https://x.test')).toEqual([{ type: 'text', text: '[a](https://x.test' }])
    expect(inline('****')).toEqual([{ type: 'text', text: '****' }])
  })

  it('survives deep nesting and very long input', () => {
    const deep = '*'.repeat(500) + 'x' + '*'.repeat(500)
    expect(() => parseMiniMarkdown(deep)).not.toThrow()

    const long = '**a** [b](https://x.test) *c* `d` '.repeat(50_000)
    const started = performance.now()
    const out = parseMiniMarkdown(long)
    expect(performance.now() - started).toBeLessThan(1000)
    const text = JSON.stringify(out)
    expect(text.length).toBeLessThan(MAX_SOURCE_LENGTH * 40)

    const manyOpeners = '*'.repeat(MAX_SOURCE_LENGTH * 4)
    expect(() => parseMiniMarkdown(manyOpeners)).not.toThrow()
  })
})

describe('<MiniMarkdown>', () => {
  it('renders elements with safe link attributes and no injected markup', () => {
    const wrapper = mount(MiniMarkdown, {
      props: { source: 'Hello **world**, see [docs](https://example.com).\n\n<img src=x onerror=alert(1)> [bad](javascript:alert(1))' },
    })
    const link = wrapper.get('a')
    expect(link.attributes('href')).toBe('https://example.com')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener noreferrer')
    expect(wrapper.findAll('a')).toHaveLength(1)
    expect(wrapper.find('strong').text()).toBe('world')
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).toContain('<img src=x onerror=alert(1)>')
    expect(wrapper.text()).toContain('[bad](javascript:alert(1))')
    expect(wrapper.findAll('p')).toHaveLength(2)
  })
})
