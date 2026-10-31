// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { existsSync } from 'node:fs'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import pixelmatch from 'pixelmatch'
import type { ElementHandle } from 'puppeteer-core'
import { PNG } from 'pngjs'
import { RESULTS_DIR } from './browser'
import type { Ui } from './ui'

/** Baselines, committed next to the specs. */
export const BASELINE_DIR = join(import.meta.dirname, '..', 'chromium', '__screenshots__')

/**
 * Tolerance of a comparison. `colorThreshold` is pixelmatch's per-pixel colour
 * distance (0 exact, 1 anything): 0.1 absorbs the sub-pixel anti-aliasing
 * differences between GPU-less rasterisers. `maxDiffRatio` is the share of
 * pixels that may still differ: 0.1% passes a stray glyph edge and fails a
 * moved icon, a changed colour or a different layout.
 */
export const TOLERANCE = { colorThreshold: 0.1, maxDiffRatio: 0.001 } as const

/** `bun run e2e:visual:update` sets this: write the baselines instead of comparing against them. */
const UPDATE = process.env.UPDATE_SNAPSHOTS === '1'

/** Freezes everything that moves, so two runs paint the same pixels. */
export async function freeze(ui: Ui): Promise<void> {
  await ui.page.addStyleTag({
    content: '*, *::before, *::after { animation: none !important; transition: none !important; caret-color: transparent !important; scroll-behavior: auto !important; }',
  })
  // Web fonts are part of the picture: wait for them instead of painting the fallback face.
  await ui.page.evaluate(() => document.fonts.ready.then(() => undefined))
}

/**
 * Compares a PNG with the stored baseline `<name>.png` (or writes it under
 * UPDATE_SNAPSHOTS=1). A mismatch leaves `<name>.{expected,actual,diff}.png`
 * in test-results/visual, which CI uploads, and fails with the pixel count.
 */
export async function expectMatchesBaseline(name: string, actual: Uint8Array): Promise<void> {
  const file = join(BASELINE_DIR, `${name}.png`)
  if (UPDATE) {
    await mkdir(BASELINE_DIR, { recursive: true })
    await writeFile(file, actual)
    return
  }
  if (!existsSync(file)) throw new Error(`no baseline for ${name}: run \`bun run e2e:visual:update\` and commit ${file}`)
  const expected = PNG.sync.read(Buffer.from(await readFile(file)))
  const got = PNG.sync.read(Buffer.from(actual))
  if (expected.width !== got.width || expected.height !== got.height) {
    await keep(name, expected, got, undefined)
    throw new Error(`${name}: ${got.width}x${got.height} px, the baseline is ${expected.width}x${expected.height} px`)
  }
  const diff = new PNG({ width: got.width, height: got.height })
  const differing = pixelmatch(expected.data, got.data, diff.data, got.width, got.height, { threshold: TOLERANCE.colorThreshold })
  const ratio = differing / (got.width * got.height)
  if (ratio > TOLERANCE.maxDiffRatio) {
    await keep(name, expected, got, diff)
    throw new Error(`${name}: ${differing} px differ (${(ratio * 100).toFixed(3)}%, at most ${TOLERANCE.maxDiffRatio * 100}% allowed); see test-results/visual/${name}.diff.png`)
  }
}

async function keep(name: string, expected: PNG, actual: PNG, diff: PNG | undefined): Promise<void> {
  const dir = join(RESULTS_DIR, 'visual')
  await mkdir(dir, { recursive: true })
  await writeFile(join(dir, `${name}.expected.png`), PNG.sync.write(expected))
  await writeFile(join(dir, `${name}.actual.png`), PNG.sync.write(actual))
  if (diff !== undefined) await writeFile(join(dir, `${name}.diff.png`), PNG.sync.write(diff))
}

/**
 * The first toast, once it is past its enter transition (opacity 0 and shifted
 * down 12px at first): only then is its box, and the colour blended with the
 * page behind it, the one at rest.
 */
export async function toastAtRest(ui: Ui): Promise<ElementHandle<Element>> {
  const toast = await ui.page.waitForSelector('[role="region"][aria-live] > div', { visible: true })
  if (toast === null) throw new Error('no toast appeared')
  await ui.page.waitForFunction(
    (el) => {
      const style = getComputedStyle(el)
      return style.opacity === '1' && (style.transform === 'none' || style.transform === 'matrix(1, 0, 0, 1, 0, 0)')
    },
    {},
    toast,
  )
  return toast
}

/**
 * Waits until the page has loaded nothing for `idleMs`, then for the web fonts
 * and two animation frames. Puppeteer's `waitForNetworkIdle` counts in-flight
 * requests and, after a reload that aborts some, can wait for ever; this one
 * watches the finished resources instead, which cannot get stuck.
 */
export async function painted(ui: Ui, idleMs = 400): Promise<void> {
  await ui.page.evaluate(
    (idle) =>
      new Promise<void>((resolve) => {
        let timer = setTimeout(done, idle)
        const observer = new PerformanceObserver(() => {
          clearTimeout(timer)
          timer = setTimeout(done, idle)
        })
        observer.observe({ type: 'resource' })
        function done(): void {
          observer.disconnect()
          void document.fonts.ready.then(() => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
        }
      }),
    idleMs,
  )
}
