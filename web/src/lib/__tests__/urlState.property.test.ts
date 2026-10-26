// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from 'bun:test'
import { parseQuery, stringifyQuery, type LocationQuery } from 'vue-router'
import { boolParam, enumParam, intParam, listParam, readQuery, strParam, writeQuery, type Codec, type Schema, type Values } from '../../composables/useUrlState'
import { fc, hostileString } from './propertySetup'

// The query-string codecs behind useUrlState and useListQuery. The composables
// add only router glue on top of readQuery/writeQuery, so the contract is
// tested on those and on the real vue-router query (de)serializer.

const text = fc.oneof(hostileString, fc.string({ unit: 'grapheme' }))
const SORTS = ['name', 'size', 'created'] as const

/** Codec plus a generator of the values it can represent and a generator of its default. */
interface Case<T> {
  label: string
  codec: Codec<T>
  value: fc.Arbitrary<T>
}

const strCase = fc.string({ unit: 'grapheme', maxLength: 6 }).chain((def) =>
  fc.constant<Case<string>>({ label: 'str', codec: strParam(def), value: fc.oneof(text.filter((s) => s !== ''), fc.constant(def)) }),
)
const intCase = fc.tuple(fc.integer({ min: -50, max: 50 }), fc.integer({ min: -50, max: 5 })).chain(([def0, min]) => {
  const def = Math.max(def0, min)
  return fc.constant<Case<number>>({ label: 'int', codec: intParam(def, min), value: fc.integer({ min, max: Number.MAX_SAFE_INTEGER }) })
})
const boolCase = fc.boolean().map<Case<boolean>>((def) => ({ label: 'bool', codec: boolParam(def), value: fc.boolean() as never }))
const enumCase = fc.constantFrom(...SORTS).map<Case<string>>((def) => ({ label: 'enum', codec: enumParam(SORTS, def), value: fc.constantFrom(...SORTS) }))
const item = text.filter((s) => s !== '' && !s.includes(','))
const listCase = fc.array(item, { maxLength: 3 }).map<Case<string[]>>((def) => ({
  label: 'list',
  codec: listParam(def),
  // An empty list is the same as "not set": it reads back as the default.
  value: fc.array(item, { minLength: def.length ? 1 : 0, maxLength: 4 }),
}))

// A codec of any type; the cast keeps the heterogeneous schema simple.
const anyCase = fc.oneof(strCase, intCase, boolCase, enumCase, listCase) as fc.Arbitrary<Case<unknown>>

/** A schema of 1-5 distinctly named parameters and a generator of states that respect their value domains. */
const schemaAndState = fc
  .uniqueArray(fc.stringMatching(/^[a-z][a-z0-9]{0,6}$/).filter((k) => k !== 'constructor' && k !== 'toString'), { minLength: 1, maxLength: 5 })
  .chain((keys) =>
    fc.array(anyCase, { minLength: keys.length, maxLength: keys.length }).chain((cases) => {
      const schema: Schema = Object.fromEntries(keys.map((k, i) => [k, cases[i]!.codec]))
      const state = fc.tuple(...cases.map((c) => c.value)).map((vals) => Object.fromEntries(keys.map((k, i) => [k, vals[i]])))
      return fc.record({ schema: fc.constant(schema), state })
    }),
  )

const queryValue: fc.Arbitrary<LocationQuery[string]> = fc.oneof(text, fc.constant(null), fc.array(fc.oneof(text, fc.constant(null)), { maxLength: 3 }))

describe('codecs (property)', () => {
  it('parse(serialize(x)) equals x for every representable value', () => {
    fc.assert(fc.property(anyCase.chain((c) => c.value.map((v) => [c.codec, v] as const)), ([codec, v]) => {
      expect(codec.parse(codec.serialize(v))).toEqual(v)
    }))
  })

  it('never serializes the default, and an absent parameter reads as the default', () => {
    fc.assert(fc.property(anyCase, (c) => {
      expect(c.codec.serialize(c.codec.default)).toBeNull()
      expect(c.codec.parse(null)).toEqual(c.codec.default)
    }))
  })

  it('reads any raw text without throwing, and what it reads re-serializes to a fixed point', () => {
    fc.assert(fc.property(anyCase, fc.option(text, { nil: null }), (c, raw) => {
      const v = c.codec.parse(raw)
      const again = c.codec.parse(c.codec.serialize(v))
      expect(again === v || JSON.stringify(again) === JSON.stringify(v)).toBe(true)
    }))
  })

  it('reads an empty string as the default for free text', () => {
    fc.assert(fc.property(fc.string({ unit: 'grapheme' }), (def) => {
      expect(strParam(def).parse('')).toBe(def)
      expect(strParam(def).serialize('')).toBeNull()
    }))
  })

  it('falls back to the default for integers below the minimum or not written as digits', () => {
    fc.assert(fc.property(fc.integer({ min: 1, max: 20 }), text, (def, raw) => {
      const codec = intParam(def, 1)
      fc.pre(!/^-?\d+$/.test(raw))
      expect(codec.parse(raw)).toBe(def)
      expect(codec.parse('0')).toBe(def)
      expect(codec.parse('-3')).toBe(def)
    }))
  })

  it('keeps enum values inside the declared set whatever the URL says', () => {
    fc.assert(fc.property(fc.option(text, { nil: null }), (raw) => {
      expect(SORTS as readonly string[]).toContain(enumParam(SORTS, 'name').parse(raw))
    }))
  })
})

describe('readQuery / writeQuery (property)', () => {
  it('restores any state written to the URL, through the real query-string serializer', () => {
    fc.assert(fc.property(schemaAndState, ({ schema, state }) => {
      const written = writeQuery(schema, {}, state as Partial<Values<typeof schema>>)
      const roundTripped = parseQuery(stringifyQuery(written))
      expect(readQuery(schema, roundTripped)).toEqual(state as never)
    }))
  })

  it('omits every parameter that equals its default and writes only schema keys', () => {
    fc.assert(fc.property(schemaAndState, ({ schema, state }) => {
      const written = writeQuery(schema, {}, state as Partial<Values<typeof schema>>)
      for (const key of Object.keys(written)) {
        expect(Object.keys(schema)).toContain(key)
        expect(schema[key]!.serialize(schema[key]!.default)).toBeNull()
        expect(JSON.stringify(state[key])).not.toBe(JSON.stringify(schema[key]!.default))
      }
      for (const key of Object.keys(schema)) {
        if (JSON.stringify(state[key]) === JSON.stringify(schema[key]!.default)) expect(key in written).toBe(false)
      }
    }))
  })

  it('leaves parameters outside the schema untouched', () => {
    fc.assert(fc.property(schemaAndState, fc.dictionary(fc.stringMatching(/^[A-Z][A-Za-z0-9]{0,6}$/), queryValue, { maxKeys: 4 }), ({ schema, state }, foreign) => {
      const written = writeQuery(schema, foreign, state as Partial<Values<typeof schema>>)
      for (const [k, v] of Object.entries(foreign)) expect(written[k]).toEqual(v)
    }))
  })

  it('does not mutate the query it is given', () => {
    fc.assert(fc.property(schemaAndState, fc.dictionary(fc.stringMatching(/^[a-z]{1,4}$/), queryValue, { maxKeys: 4 }), ({ schema, state }, query) => {
      const before = JSON.stringify(query)
      writeQuery(schema, query, state as Partial<Values<typeof schema>>)
      expect(JSON.stringify(query)).toBe(before)
    }))
  })

  it('reads arbitrary or garbage queries without throwing, always yielding a value for every schema key', () => {
    fc.assert(fc.property(schemaAndState, fc.dictionary(fc.string({ maxLength: 5 }), queryValue, { maxKeys: 6 }), ({ schema }, query) => {
      const read = readQuery(schema, query)
      expect(Object.keys(read).sort()).toEqual(Object.keys(schema).sort())
      // What was read is itself representable: writing it and reading it back changes nothing.
      const again = readQuery(schema, parseQuery(stringifyQuery(writeQuery(schema, {}, read))))
      expect(again).toEqual(read)
    }))
  })

  it('reads the first value when a key is repeated in the URL', () => {
    fc.assert(fc.property(item, item, (a, b) => {
      expect(readQuery({ q: strParam() }, { q: [a, b] })).toEqual({ q: a })
    }))
  })
})
