// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Shared fast-check configuration for the *.property.test.ts files. Import it
// for its side effect. A failing property prints its seed and counterexample;
// replay one with `FC_SEED=<seed> bun test <file>` and raise the number of
// generated cases (default 100, kept low so the unit suite stays fast) with
// `FC_RUNS=2000`.
import fc from 'fast-check'

const seed = Number(process.env.FC_SEED)
const runs = Number(process.env.FC_RUNS)

fc.configureGlobal({
  numRuns: runs > 0 ? runs : 100,
  ...(Number.isInteger(seed) && process.env.FC_SEED ? { seed } : {}),
})

/** Characters that break an unquoted shell word, a URL or markup if a value is spliced in unescaped. */
const HOSTILE = [' ', '\t', '\n', '\r', "'", '"', '`', '$', '\\', ';', '&', '|', '(', ')', '<', '>', '*', '?', '[', ']', '{', '}', '~', '#', '!', '%', '/', '@', ':', '=', '+', ',', '$(id)', '`id`', "'; rm -rf / #", '\u0000', '\u00e9', '\u2028', '\u{1F600}']

/** Arbitrary text, biased toward characters that need escaping; any length from empty up to a short string. */
export const hostileString: fc.Arbitrary<string> = fc
  .array(fc.oneof({ weight: 3, arbitrary: fc.constantFrom(...HOSTILE) }, { weight: 3, arbitrary: fc.string({ unit: 'grapheme-ascii', maxLength: 6 }) }, { weight: 1, arbitrary: fc.string({ unit: 'grapheme', maxLength: 4 }) }), { maxLength: 8 })
  .map((parts) => parts.join(''))

/** Same as {@link hostileString} but never empty. */
export const hostileWord: fc.Arbitrary<string> = hostileString.filter((s) => s.length > 0)

export { fc }
