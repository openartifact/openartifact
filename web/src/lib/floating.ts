// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Positioning math of the floating layer (tooltips, menus, popovers): where a box of a given size
// goes next to an anchor rectangle inside a viewport. Pure (no DOM), so the placement rules are
// tested with fake rectangles; `useFloating` feeds it real ones.

export type Side = 'top' | 'bottom' | 'left' | 'right'
export type Align = 'start' | 'center' | 'end'
/** A side, optionally aligned to the start or the end of the anchor ("bottom-end": below, right edges flush). */
export type Placement = Side | `${Side}-start` | `${Side}-end`

export interface Rect {
  top: number
  left: number
  right: number
  bottom: number
}

export interface Size {
  width: number
  height: number
}

export interface FloatingOptions {
  placement?: Placement
  /** Gap between the anchor and the floating box, along the placement side. */
  offset?: number
  /** Minimum distance kept to the viewport edges. */
  padding?: number
}

export interface FloatingPosition {
  top: number
  left: number
  /** The side actually used: differs from the requested one when the box was flipped. */
  placement: Placement
}

const OPPOSITE: Record<Side, Side> = { top: 'bottom', bottom: 'top', left: 'right', right: 'left' }

export function parsePlacement(placement: Placement): { side: Side; align: Align } {
  const [side, align] = placement.split('-') as [Side, Align | undefined]
  return { side, align: align ?? 'center' }
}

function join(side: Side, align: Align): Placement {
  return align === 'center' ? side : `${side}-${align}`
}

/** Room on one side of the anchor, inside the viewport padding. */
function room(side: Side, anchor: Rect, viewport: Size, padding: number, offset: number): number {
  switch (side) {
    case 'top':
      return anchor.top - offset - padding
    case 'bottom':
      return viewport.height - anchor.bottom - offset - padding
    case 'left':
      return anchor.left - offset - padding
    case 'right':
      return viewport.width - anchor.right - offset - padding
  }
}

function clamp(value: number, min: number, max: number): number {
  // A box larger than the viewport sticks to the start edge (its start stays reachable).
  return Math.max(min, Math.min(value, max))
}

/**
 * Viewport (fixed) coordinates of a box of `size` placed next to `anchor`.
 *
 * The preferred side is used while the box fits there; otherwise it flips to the opposite side
 * when that fits, else to whichever of the two has more room. Along the other axis the box is
 * aligned as asked and then shifted back inside the viewport padding.
 */
export function computePosition(anchor: Rect, size: Size, viewport: Size, options: FloatingOptions = {}): FloatingPosition {
  const { placement = 'bottom', offset = 4, padding = 8 } = options
  const { side: preferred, align } = parsePlacement(placement)
  const vertical = preferred === 'top' || preferred === 'bottom'
  const need = vertical ? size.height : size.width

  const opposite = OPPOSITE[preferred]
  const roomPreferred = room(preferred, anchor, viewport, padding, offset)
  const roomOpposite = room(opposite, anchor, viewport, padding, offset)
  let side = preferred
  if (roomPreferred < need && (roomOpposite >= need || roomOpposite > roomPreferred)) side = opposite

  let top: number
  let left: number
  if (vertical) {
    top = side === 'bottom' ? anchor.bottom + offset : anchor.top - offset - size.height
    left = alignedStart(anchor.left, anchor.right, size.width, align)
  } else {
    left = side === 'right' ? anchor.right + offset : anchor.left - offset - size.width
    top = alignedStart(anchor.top, anchor.bottom, size.height, align)
  }

  // Shift along the cross axis so the box stays on screen.
  if (vertical) left = clamp(left, padding, viewport.width - size.width - padding)
  else top = clamp(top, padding, viewport.height - size.height - padding)

  return { top, left, placement: join(side, align) }
}

/** Start coordinate of a box aligned to the [start, end] span of the anchor along one axis. */
function alignedStart(start: number, end: number, length: number, align: Align): number {
  if (align === 'start') return start
  if (align === 'end') return end - length
  return start + (end - start) / 2 - length / 2
}
