// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import type { Ui } from './ui'

const require = createRequire(import.meta.url)
const AXE_SOURCE = readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8')

/**
 * Rules that need a rendering engine. Lightpanda computes no layout (boxes are
 * approximations, nothing is painted), so contrast, target size and scroll
 * geometry cannot be judged:
 * these rules are switched off, not guessed. Colour contrast of both themes
 * is therefore NOT covered by the e2e suite.
 */
export const LAYOUT_DEPENDENT_RULES = [
  'color-contrast',
  'color-contrast-enhanced',
  'target-size',
  'scrollable-region-focusable',
  'link-in-text-block',
] as const

export interface AxeViolation {
  id: string
  impact: string
  help: string
  nodes: string[]
}

/**
 * Runs axe-core (injected into the page as a script) with the layout-independent rules of
 * WCAG 2.x A/AA and the best-practice set, and returns the violations of
 * impact `serious` or `critical`.
 */
export async function seriousViolations(ui: Ui, exclude: string[] = []): Promise<AxeViolation[]> {
  await ui.page.evaluate(AXE_SOURCE)
  const rules = Object.fromEntries(LAYOUT_DEPENDENT_RULES.map((id) => [id, { enabled: false }]))
  const found = await ui.page.evaluate(
    async ({ rules, exclude }) => {
      const axe = (globalThis as unknown as { axe: { run(ctx: unknown, opts: unknown): Promise<{ violations: Array<{ id: string; impact: string | null; help: string; nodes: Array<{ target: unknown[] }> }> }> } }).axe
      const context = exclude.length > 0 ? { include: [document], exclude: exclude.map((s) => [s]) } : document
      const res = await axe.run(context, {
        runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'] },
        rules,
        resultTypes: ['violations'],
      })
      return res.violations.map((v) => ({ id: v.id, impact: v.impact ?? 'minor', help: v.help, nodes: v.nodes.map((n) => n.target.join(' ')) }))
    },
    { rules, exclude },
  )
  return found.filter((v) => v.impact === 'serious' || v.impact === 'critical')
}
