// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * Class lists shared by every table so one rule keeps the row actions reachable.
 * They assume the table sits in a `TableScroller` (the `group/ts` scroll container with
 * `data-stuck`) and the row carries `group/row`. The `*_MD` variants are for `ListTable`,
 * which turns into stacked cards below 768px and so applies them from `md` up.
 */

/** Actions cell: pinned to the right edge of the scroll container with a solid fill, so it stays visible however narrow the table gets. */
export const ACTIONS_CELL =
  'sticky right-0 z-[1] border-l border-transparent bg-surface text-right group-hover/row:bg-surface-hover group-data-[stuck]/ts:border-border'
export const ACTIONS_CELL_MD =
  'md:sticky md:right-0 md:z-[1] md:border-l md:border-transparent md:bg-surface md:text-right md:group-hover/row:bg-surface-hover md:group-data-[stuck]/ts:border-border'

/** Actions header: same pin, on the card fill (a hand-built header has no fill of its own). */
export const ACTIONS_HEAD = 'sticky right-0 z-[1] border-l border-transparent bg-surface group-data-[stuck]/ts:border-border'
/** Actions header for `ListTable`, whose header row is filled with `bg-surface-hover`. */
export const ACTIONS_HEAD_MD =
  'md:sticky md:right-0 md:z-[1] md:border-l md:border-transparent md:bg-surface-hover md:group-data-[stuck]/ts:border-border'

/**
 * The identifier column: it truncates instead of stretching the table. `max-w-0` removes the
 * text's own width from the column sizing (a table cell never shrinks below its nowrap text
 * otherwise) and `w-[40%]` is what the column asks for; the other columns keep their natural
 * width and only squeeze (and wrap) when the table is short of room. `min-w-56` is the floor
 * that keeps an identifier readable.
 */
export const GROW_CELL = 'w-[40%] min-w-56 max-w-0'
export const GROW_CELL_MD = 'md:w-[40%] md:min-w-56 md:max-w-0'
