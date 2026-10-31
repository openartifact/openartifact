// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from 'bun:test'
import { computePosition, parsePlacement, type Rect } from '../floating'

const viewport = { width: 1000, height: 800 }
const box = { width: 100, height: 40 }
const at = (left: number, top: number, width = 20, height = 20): Rect => ({ left, top, right: left + width, bottom: top + height })

describe('parsePlacement', () => {
  it('splits a side and an alignment, centered by default', () => {
    expect(parsePlacement('top')).toEqual({ side: 'top', align: 'center' })
    expect(parsePlacement('bottom-end')).toEqual({ side: 'bottom', align: 'end' })
    expect(parsePlacement('left-start')).toEqual({ side: 'left', align: 'start' })
  })
})

describe('computePosition', () => {
  it('centers the box on the anchor on the requested side, offset away from it', () => {
    const anchor = at(400, 400)
    expect(computePosition(anchor, box, viewport, { placement: 'bottom', offset: 6 })).toEqual({ top: 426, left: 360, placement: 'bottom' })
    expect(computePosition(anchor, box, viewport, { placement: 'top', offset: 6 })).toEqual({ top: 354, left: 360, placement: 'top' })
    expect(computePosition(anchor, box, viewport, { placement: 'right', offset: 6 })).toEqual({ top: 390, left: 426, placement: 'right' })
    expect(computePosition(anchor, box, viewport, { placement: 'left', offset: 6 })).toEqual({ top: 390, left: 294, placement: 'left' })
  })

  it('aligns to the start or the end edge of the anchor', () => {
    const anchor = at(400, 400, 40)
    expect(computePosition(anchor, box, viewport, { placement: 'bottom-start' }).left).toBe(400)
    // End: the right edges are flush (a row-actions menu hangs from its button's right edge).
    expect(computePosition(anchor, box, viewport, { placement: 'bottom-end' }).left).toBe(340)
  })

  it('flips to the other side when the preferred one has no room and the other has', () => {
    // 10px under the anchor: a 40px tooltip below does not fit above the viewport bottom.
    const nearBottom = at(400, 770)
    expect(computePosition(nearBottom, box, viewport, { placement: 'bottom', offset: 4 })).toEqual({ top: 726, left: 360, placement: 'top' })
    const nearTop = at(400, 5)
    expect(computePosition(nearTop, box, viewport, { placement: 'top', offset: 4 })).toEqual({ top: 29, left: 360, placement: 'bottom' })
    const nearRight = at(960, 400)
    expect(computePosition(nearRight, box, viewport, { placement: 'right', offset: 4 }).placement).toBe('left')
    const nearLeft = at(4, 400)
    expect(computePosition(nearLeft, box, viewport, { placement: 'left', offset: 4 }).placement).toBe('right')
  })

  it('keeps the requested alignment when it flips', () => {
    expect(computePosition(at(400, 770), box, viewport, { placement: 'bottom-end' }).placement).toBe('top-end')
  })

  it('does not flip when the preferred side fits exactly', () => {
    // 800 - 8 padding - 4 offset - 40 height = 748 is the last anchor bottom that fits below.
    const anchor = at(400, 728)
    expect(anchor.bottom).toBe(748)
    expect(computePosition(anchor, box, viewport, { placement: 'bottom', offset: 4, padding: 8 }).placement).toBe('bottom')
    expect(computePosition({ ...anchor, top: 729, bottom: 749 }, box, viewport, { placement: 'bottom', offset: 4, padding: 8 }).placement).toBe('top')
  })

  it('takes the side with more room when the box fits on neither', () => {
    const tall = { width: 100, height: 700 }
    // Anchor at 300..320: 472 below, 288 above. The box fits on neither side; below has more room.
    expect(computePosition(at(400, 300), tall, viewport, { placement: 'top' }).placement).toBe('bottom')
    expect(computePosition(at(400, 500), tall, viewport, { placement: 'bottom' }).placement).toBe('top')
  })

  it('shifts along the edge so the box stays inside the padding', () => {
    expect(computePosition(at(2, 400), box, viewport, { placement: 'bottom', padding: 8 }).left).toBe(8)
    expect(computePosition(at(985, 400), box, viewport, { placement: 'bottom', padding: 8 }).left).toBe(1000 - 100 - 8)
    // Sideways placements shift vertically.
    expect(computePosition(at(400, 2), box, viewport, { placement: 'right', padding: 8 }).top).toBe(8)
    expect(computePosition(at(400, 790), box, viewport, { placement: 'right', padding: 8 }).top).toBe(800 - 40 - 8)
  })

  it('keeps the start of a box wider than the viewport reachable', () => {
    const wide = { width: 1200, height: 40 }
    expect(computePosition(at(400, 400), wide, viewport, { placement: 'bottom' }).left).toBe(8)
  })

  it('defaults to an unflipped bottom placement with a 4px gap', () => {
    expect(computePosition(at(400, 100), box, viewport)).toEqual({ top: 124, left: 360, placement: 'bottom' })
  })
})
