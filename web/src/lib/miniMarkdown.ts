// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Renderer for the tiny markdown subset allowed in the login message: blank-line
// separated paragraphs, line breaks, **bold**, *italic*, `code` and
// [text](url) with an http:, https: or mailto: URL. The output is a tree of
// typed nodes for a Vue component to render as elements and text; nothing is
// ever turned into HTML, so markup in the input shows up literally.

export type InlineNode =
  | { type: 'text'; text: string }
  | { type: 'br' }
  | { type: 'bold'; children: InlineNode[] }
  | { type: 'italic'; children: InlineNode[] }
  | { type: 'code'; text: string }
  | { type: 'link'; href: string; children: InlineNode[] }

export interface Paragraph {
  type: 'paragraph'
  children: InlineNode[]
}

/** The server caps the message at 2000 characters; anything beyond this is cut before parsing. */
export const MAX_SOURCE_LENGTH = 4000
const MAX_DEPTH = 4
const LINK_SCHEMES = new Set(['http:', 'https:', 'mailto:'])

function safeHref(raw: string): string | null {
  if (/\s/.test(raw)) return null
  try {
    return LINK_SCHEMES.has(new URL(raw).protocol) ? raw : null
  } catch {
    return null
  }
}

function pushText(out: InlineNode[], text: string): void {
  if (!text) return
  const last = out[out.length - 1]
  if (last?.type === 'text') last.text += text
  else out.push({ type: 'text', text })
}

function parseInline(src: string, depth: number, allowLinks: boolean): InlineNode[] {
  const out: InlineNode[] = []
  let i = 0
  while (i < src.length) {
    const ch = src[i]!

    if (ch === '\n') {
      out.push({ type: 'br' })
      i++
      continue
    }

    if (ch === '`') {
      const end = src.indexOf('`', i + 1)
      if (end > i + 1) {
        out.push({ type: 'code', text: src.slice(i + 1, end) })
        i = end + 1
        continue
      }
    }

    if (ch === '[' && allowLinks && depth < MAX_DEPTH) {
      const mid = src.indexOf('](', i + 1)
      const end = mid === -1 ? -1 : src.indexOf(')', mid + 2)
      if (mid > i + 1 && end > mid + 2) {
        const label = src.slice(i + 1, mid)
        const href = safeHref(src.slice(mid + 2, end))
        if (href && !label.includes('\n')) {
          out.push({ type: 'link', href, children: parseInline(label, depth + 1, false) })
          i = end + 1
          continue
        }
      }
    }

    if (ch === '*' && depth < MAX_DEPTH) {
      const bold = src.startsWith('**', i)
      const marker = bold ? '**' : '*'
      const end = src.indexOf(marker, i + marker.length)
      if (end > i + marker.length) {
        const children = parseInline(src.slice(i + marker.length, end), depth + 1, allowLinks)
        out.push({ type: bold ? 'bold' : 'italic', children })
        i = end + marker.length
        continue
      }
    }

    // Plain character, or a marker that never closed: shown as typed.
    pushText(out, ch)
    i++
  }
  return out
}

/** Parses the message into paragraphs of inline nodes. Empty input gives no paragraphs. */
export function parseMiniMarkdown(source: string): Paragraph[] {
  const text = source.slice(0, MAX_SOURCE_LENGTH).replace(/\r\n?/g, '\n').trim()
  if (!text) return []
  return text
    .split(/\n{2,}/)
    .map((block) => block.trim())
    .filter(Boolean)
    .map((block) => ({ type: 'paragraph', children: parseInline(block, 0, true) }))
}
