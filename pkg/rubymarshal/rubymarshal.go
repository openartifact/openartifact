// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rubymarshal implements a minimal, dependency-free subset of Ruby's
// Marshal format version 4.8 (the binary wire format Marshal.dump/
// Marshal.load use — unrelated to YAML). It supports exactly the constructs
// RubyGems' legacy gemspec / specs-index protocol relies on:
//
//   - nil, true, false, Integer (Fixnum, arbitrary-width on decode), Float
//   - String, Symbol, Array, Hash (with/without a default value)
//   - generic objects                                   (tag 'o')
//   - objects with custom marshal_dump/marshal_load      (tag 'U', UserMarshal)
//   - objects with custom _dump/_load                    (tag 'u', UserDefined —
//     this is how Gem::Specification itself is dumped)
//
// Symbol and object backreferences are decoded correctly, since real
// RubyGems payloads rely on them heavily (repeated :runtime symbols, class
// names, etc). The encoder always emits full values and never backreferences
// — still valid Marshal, just less compact than MRI's own writer.
//
// Anything outside that subset (Struct, Regexp, Class/Module references, the
// pre-1.0 YAML-in-Marshal quirks, …) is out of scope: Decode returns an error
// rather than guessing.
package rubymarshal

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
)

const (
	majorVersion byte = 4
	minorVersion byte = 8
)

// maxMarshalElements caps any single array/hash/ivar-list/bignum-word count
// or byte-string/symbol length decoded from an attacker-controlled Marshal
// stream (readLong can return a negative value or one near INT32_MAX from a
// crafted 4-byte long form). Without a cap, make([]T, n) on such a count
// allocates gigabytes before a single byte of the claimed content is ever
// read, crashing the process with a fatal (non-recoverable) OOM instead of
// returning an error. 1<<20 (~1M) is far beyond anything a real gemspec or
// specs index ever needs, yet trivially cheap to allocate.
const maxMarshalElements = 1 << 20

// maxMarshalDepth caps how deeply values may nest ('I' wrappers, arrays,
// hash keys/values, object ivars, …) in an attacker-controlled Marshal
// stream. readValue recurses once per nesting level, so a stream of 4 MiB of
// 'I' bytes (each opening one more level) would otherwise grow the goroutine
// stack past Go's 1 GB limit — a fatal, unrecoverable error that takes the
// whole process down rather than failing the one upload. A real
// Gem::Specification nests roughly ten levels (spec array -> dependency
// object -> requirement array -> version); 64 leaves ample headroom.
const maxMarshalDepth = 64

// ErrUnsupported is wrapped into errors returned by Decode when the stream
// uses a Marshal construct outside this package's subset.
var ErrUnsupported = errors.New("rubymarshal: unsupported marshal construct")

// ErrInvalid reports malformed, truncated or otherwise unencodable Marshal data.
var ErrInvalid = errors.New("rubymarshal: invalid data")

// Symbol is a Ruby Symbol, e.g. :name.
type Symbol string

// HashEntry is one key/value pair of a decoded/encoded Ruby Hash. A slice of
// entries (rather than a Go map) preserves insertion order — matching Ruby's
// Hash semantics — and tolerates unhashable Go key types.
type HashEntry struct {
	Key   any
	Value any
}

// Hash is an ordered Ruby Hash.
type Hash []HashEntry

// IVar is one instance variable of an Object, e.g. {"name", "rack"} for the
// Ruby ivar @name. Name is stored WITHOUT the leading '@'.
type IVar struct {
	Name  string
	Value any
}

// Object is a generic Ruby object (Marshal tag 'o'): a class name plus its
// instance variables, in declaration order.
type Object struct {
	Class string
	IVars []IVar
}

// IVar looks up an instance variable by name (without the leading '@').
func (o *Object) IVar(name string) (any, bool) {
	for _, iv := range o.IVars {
		if iv.Name == name {
			return iv.Value, true
		}
	}

	return nil, false
}

// UserMarshal is an object whose class defines marshal_dump/marshal_load
// (Marshal tag 'U'). Payload is exactly the value marshal_dump returned.
type UserMarshal struct {
	Class   string
	Payload any
}

// UserDefined is an object whose class defines _dump/_load (Marshal tag
// 'u'). Data is the raw byte string _dump returned — often itself a full,
// independent Marshal stream, as is the case for Gem::Specification.
type UserDefined struct {
	Class string
	Data  []byte
}

// Encode serializes v into a complete Marshal 4.8 byte stream, including the
// 2-byte version header.
func Encode(v any) ([]byte, error) {
	e := &encoder{buf: []byte{majorVersion, minorVersion}}
	e.writeValue(v)

	if e.err != nil {
		return nil, e.err
	}

	return e.buf, nil
}

// Decode parses a complete Marshal 4.8 byte stream and returns the root
// value using the Go types documented above (nil, bool, int64, float64,
// string, Symbol, []any, Hash, *Object, *UserMarshal, *UserDefined,
// *big.Int for Bignum).
func Decode(data []byte) (any, error) {
	if len(data) < 2 || data[0] != majorVersion || data[1] != minorVersion {
		return nil, fmt.Errorf("%w: not a Marshal 4.8 stream", ErrInvalid)
	}

	d := &decoder{data: data, pos: 2}

	return d.readValue()
}

// ---- encoder --------------------------------------------------------------

type encoder struct {
	buf []byte
	err error
}

func (e *encoder) writeValue(v any) {
	if e.err != nil {
		return
	}

	switch t := v.(type) {
	case nil:
		e.buf = append(e.buf, '0')
	case bool:
		if t {
			e.buf = append(e.buf, 'T')
		} else {
			e.buf = append(e.buf, 'F')
		}
	case int:
		e.writeFixnum(int64(t))
	case int64:
		e.writeFixnum(t)
	case float64:
		e.writeFloat(t)
	case string:
		e.writeIVarString(t)
	case Symbol:
		e.writeSymbol(string(t))
	case []any:
		e.writeArray(t)
	case Hash:
		e.writeHash(t)
	case Object:
		e.writeObject(t)
	case *Object:
		e.writeObject(*t)
	case UserMarshal:
		e.writeUserMarshal(t)
	case *UserMarshal:
		e.writeUserMarshal(*t)
	case UserDefined:
		e.writeUserDefined(t)
	case *UserDefined:
		e.writeUserDefined(*t)
	default:
		e.err = fmt.Errorf("%w: Encode: unsupported type %T", ErrInvalid, v)
	}
}

// writeLong implements MRI's w_long: the shared variable-length integer
// encoding Marshal uses for Fixnums, lengths, counts and link indices.
func (e *encoder) writeLong(n int64) {
	switch {
	case n == 0:
		e.buf = append(e.buf, 0)

		return
	case n > 0 && n < 123:
		e.buf = append(e.buf, byte(n+5))

		return
	case n < 0 && n > -124:
		e.buf = append(e.buf, byte(n-5)) //nolint:gosec // intentional 2's-complement byte cast

		return
	}

	// Real Ruby caps Fixnum long-form magnitude at 4 bytes (see marshal.c
	// w_long / ruby_marshal_write_long): byte-length values 5-8 would be
	// indistinguishable from the short-form range above, so anything wider
	// is out of scope (would need Bignum, tag 'l', which this package only
	// decodes, never encodes).
	var b [4]byte

	length := 0
	x := n

	for i := 1; i <= 4; i++ {
		b[i-1] = byte(x & 0xff)
		x >>= 8

		if x == 0 {
			length = i

			break
		}

		if x == -1 {
			length = -i

			break
		}
	}

	if length == 0 {
		e.err = fmt.Errorf("%w: integer %d exceeds Fixnum long-form range (needs Bignum)", ErrInvalid, n)

		return
	}

	e.buf = append(e.buf, byte(length)) //nolint:gosec // length is a small signed count

	n2 := length
	if n2 < 0 {
		n2 = -n2
	}

	e.buf = append(e.buf, b[:n2]...)
}

func (e *encoder) writeFixnum(n int64) {
	e.buf = append(e.buf, 'i')
	e.writeLong(n)
}

func (e *encoder) writeFloat(f float64) {
	e.buf = append(e.buf, 'f')

	var s string

	switch {
	case math.IsNaN(f):
		s = "nan"
	case math.IsInf(f, 1):
		s = "inf"
	case math.IsInf(f, -1):
		s = "-inf"
	default:
		s = strconv.FormatFloat(f, 'g', -1, 64)
	}

	e.writeCounted(s)
}

// writeCounted writes a Marshal length-prefixed byte body with no leading
// type tag — the shared shape behind String, Symbol and Float payloads.
func (e *encoder) writeCounted(s string) {
	e.writeLong(int64(len(s)))
	e.buf = append(e.buf, s...)
}

func (e *encoder) writeBareString(s string) {
	e.buf = append(e.buf, '"')
	e.writeCounted(s)
}

// writeIVarString wraps s the way MRI wraps every UTF-8 string literal: tag
// 'I' plus one ivar, :E => true, marking the encoding as UTF-8.
func (e *encoder) writeIVarString(s string) {
	e.buf = append(e.buf, 'I')
	e.writeBareString(s)
	e.writeLong(1)
	e.writeSymbol("E")
	e.writeValue(true)
}

func (e *encoder) writeSymbol(s string) {
	e.buf = append(e.buf, ':')
	e.writeLong(int64(len(s)))
	e.buf = append(e.buf, s...)
}

func (e *encoder) writeArray(arr []any) {
	e.buf = append(e.buf, '[')
	e.writeLong(int64(len(arr)))

	for _, v := range arr {
		e.writeValue(v)
	}
}

func (e *encoder) writeHash(h Hash) {
	e.buf = append(e.buf, '{')
	e.writeLong(int64(len(h)))

	for _, entry := range h {
		e.writeValue(entry.Key)
		e.writeValue(entry.Value)
	}
}

func (e *encoder) writeObject(o Object) {
	e.buf = append(e.buf, 'o')
	e.writeSymbol(o.Class)
	e.writeLong(int64(len(o.IVars)))

	for _, iv := range o.IVars {
		e.writeSymbol("@" + iv.Name)
		e.writeValue(iv.Value)
	}
}

func (e *encoder) writeUserMarshal(u UserMarshal) {
	e.buf = append(e.buf, 'U')
	e.writeSymbol(u.Class)
	e.writeValue(u.Payload)
}

func (e *encoder) writeUserDefined(u UserDefined) {
	e.buf = append(e.buf, 'u')
	e.writeSymbol(u.Class)
	e.writeLong(int64(len(u.Data)))
	e.buf = append(e.buf, u.Data...)
}

// ---- decoder --------------------------------------------------------------

type decoder struct {
	data    []byte
	pos     int
	depth   int
	symbols []string
	objects []any
}

func (d *decoder) readByte() (byte, error) {
	if d.pos >= len(d.data) {
		return 0, io.ErrUnexpectedEOF
	}

	b := d.data[d.pos]
	d.pos++

	return b, nil
}

func (d *decoder) readBytes(n int64) ([]byte, error) {
	// Compare against the remaining length instead of computing d.pos+n:
	// n comes straight from readLong and can be an attacker-chosen value
	// near math.MaxInt64, which would overflow that addition and wrap
	// negative, defeating the bounds check and panicking on the slice
	// expression below instead of returning an error.
	if n < 0 || n > int64(len(d.data)-d.pos) {
		return nil, io.ErrUnexpectedEOF
	}

	b := d.data[d.pos : d.pos+int(n)]
	d.pos += int(n)

	return b, nil
}

// readLong implements MRI's r_long0: the decoder side of writeLong. Long-form
// byte counts are always 1..4 in real Marshal streams (see writeLong's doc
// comment on the 4-byte cap), so c in 5..127 (positive) / -128..-5 (negative)
// unambiguously means short form.
func (d *decoder) readLong() (int64, error) {
	ub, err := d.readByte()
	if err != nil {
		return 0, err
	}

	c := int(int8(ub)) //nolint:gosec // intentional sign-reinterpretation of the tag byte

	switch {
	case c == 0:
		return 0, nil
	case c > 4 && c < 128:
		return int64(c - 5), nil
	case c > -129 && c < -4:
		return int64(c + 5), nil
	case c > 0: // c in 1..4: positive long form
		var x int64
		for i := range c {
			b, err := d.readByte()
			if err != nil {
				return 0, err
			}

			x |= int64(b) << (8 * i)
		}

		return x, nil
	default: // c in -1..-4: negative long form
		n := -c

		x := int64(-1)
		for i := range n {
			b, err := d.readByte()
			if err != nil {
				return 0, err
			}

			x &^= int64(0xff) << (8 * i)
			x |= int64(b) << (8 * i)
		}

		return x, nil
	}
}

// checkCount rejects a decoded length/count that is negative (readLong can
// decode a negative value from a crafted tag byte) or implausibly large
// (e.g. a claimed ~2^31 element count). Every call site that would otherwise
// make()/append-grow a slice or loop directly on such a count MUST run it
// through checkCount first, so a hostile length fails with an error instead
// of panicking (negative len to make/slice) or triggering a fatal,
// unrecoverable Go OOM (make([]T, hugeN) for a T wider than one byte).
func checkCount(n int64) error {
	if n < 0 {
		return fmt.Errorf("%w: negative length %d", ErrInvalid, n)
	}

	if n > maxMarshalElements {
		return fmt.Errorf("%w: length %d exceeds maximum %d", ErrInvalid, n, maxMarshalElements)
	}

	return nil
}

// initialCap turns an already-validated (checkCount'd) count into a slice
// capacity hint: small enough that a claimed-huge-but-unbacked count never
// over-allocates ahead of actually reading that many elements, since the
// loop appends (and thus grows the slice incrementally) rather than
// pre-sizing to n and indexing into it.
func initialCap(n int64) int {
	const capHint = 64
	if n < capHint {
		return int(n)
	}

	return capHint
}

func (d *decoder) reserve() int {
	d.objects = append(d.objects, nil)

	return len(d.objects) - 1
}

// readValue decodes one value, bounding the recursion depth (see
// maxMarshalDepth).
func (d *decoder) readValue() (any, error) {
	if d.depth >= maxMarshalDepth {
		return nil, fmt.Errorf("%w: nesting deeper than %d levels", ErrInvalid, maxMarshalDepth)
	}

	d.depth++
	v, err := d.decodeValue()
	d.depth--

	return v, err
}

func (d *decoder) decodeValue() (any, error) {
	tag, err := d.readByte()
	if err != nil {
		return nil, err
	}

	switch tag {
	case '0':
		return nil, nil //nolint:nilnil // Marshal nil decodes to Go nil, by design
	case 'T':
		return true, nil
	case 'F':
		return false, nil
	case 'i':
		return d.readLong()
	case 'l':
		return d.readBignum()
	case 'f':
		return d.readFloatTag()
	case '"':
		return d.readStringTagged()
	case ':':
		return d.readSymbolNew()
	case ';':
		return d.readSymbolLink()
	case '[':
		return d.readArrayTagged()
	case '{':
		return d.readHashTagged(false)
	case '}':
		return d.readHashTagged(true)
	case 'I':
		return d.readIVarWrapped()
	case 'o':
		return d.readObjectTagged()
	case 'U':
		return d.readUserMarshalTagged()
	case 'u':
		return d.readUserDefinedTagged()
	case '@':
		return d.readObjectLink()
	default:
		return nil, fmt.Errorf("%w: tag %q", ErrUnsupported, string(tag))
	}
}

func (d *decoder) readFloatTag() (any, error) {
	idx := d.reserve()

	s, err := d.readCountedString()
	if err != nil {
		return nil, err
	}

	var f float64

	switch s {
	case "nan":
		f = math.NaN()
	case "inf":
		f = math.Inf(1)
	case "-inf":
		f = math.Inf(-1)
	default:
		f, err = strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("rubymarshal: bad float %q: %w", s, err)
		}
	}

	d.objects[idx] = f

	return f, nil
}

func (d *decoder) readCountedString() (string, error) {
	n, err := d.readLong()
	if err != nil {
		return "", err
	}

	if err := checkCount(n); err != nil {
		return "", err
	}

	b, err := d.readBytes(n)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

func (d *decoder) readStringTagged() (any, error) {
	idx := d.reserve()

	s, err := d.readCountedString()
	if err != nil {
		return nil, err
	}

	d.objects[idx] = s

	return s, nil
}

func (d *decoder) readSymbolNew() (any, error) {
	s, err := d.readCountedString()
	if err != nil {
		return nil, err
	}

	d.symbols = append(d.symbols, s)

	return Symbol(s), nil
}

func (d *decoder) readSymbolLink() (any, error) {
	idx, err := d.readLong()
	if err != nil {
		return nil, err
	}

	if idx < 0 || int(idx) >= len(d.symbols) {
		return nil, fmt.Errorf("%w: bad symbol link %d", ErrInvalid, idx)
	}

	return Symbol(d.symbols[idx]), nil
}

func (d *decoder) readArrayTagged() (any, error) {
	idx := d.reserve()

	n, err := d.readLong()
	if err != nil {
		return nil, err
	}

	if err := checkCount(n); err != nil {
		return nil, err
	}

	arr := make([]any, 0, initialCap(n))

	for range n {
		v, err := d.readValue()
		if err != nil {
			return nil, err
		}

		arr = append(arr, v)
	}

	d.objects[idx] = arr

	return arr, nil
}

func (d *decoder) readHashTagged(withDefault bool) (any, error) {
	idx := d.reserve()

	n, err := d.readLong()
	if err != nil {
		return nil, err
	}

	if err := checkCount(n); err != nil {
		return nil, err
	}

	h := make(Hash, 0, initialCap(n))

	for range n {
		k, err := d.readValue()
		if err != nil {
			return nil, err
		}

		v, err := d.readValue()
		if err != nil {
			return nil, err
		}

		h = append(h, HashEntry{Key: k, Value: v})
	}

	if withDefault {
		if _, err := d.readValue(); err != nil {
			return nil, err
		}
	}

	d.objects[idx] = h

	return h, nil
}

// readIVarWrapped decodes an 'I'-wrapped value (almost always a String) and
// discards its instance variables: this package only needs the plain value,
// not string encoding metadata.
func (d *decoder) readIVarWrapped() (any, error) {
	inner, err := d.readValue()
	if err != nil {
		return nil, err
	}

	n, err := d.readLong()
	if err != nil {
		return nil, err
	}

	if err := checkCount(n); err != nil {
		return nil, err
	}

	for range n {
		if _, err := d.readValue(); err != nil {
			return nil, err
		}

		if _, err := d.readValue(); err != nil {
			return nil, err
		}
	}

	return inner, nil
}

func (d *decoder) readClassName() (string, error) {
	v, err := d.readValue()
	if err != nil {
		return "", err
	}

	sym, ok := v.(Symbol)
	if !ok {
		return "", fmt.Errorf("%w: expected symbol for class name, got %T", ErrInvalid, v)
	}

	return string(sym), nil
}

func (d *decoder) readObjectTagged() (any, error) {
	idx := d.reserve()

	class, err := d.readClassName()
	if err != nil {
		return nil, err
	}

	n, err := d.readLong()
	if err != nil {
		return nil, err
	}

	if err := checkCount(n); err != nil {
		return nil, err
	}

	obj := &Object{Class: class, IVars: make([]IVar, 0, initialCap(n))}

	for range n {
		nameVal, err := d.readValue()
		if err != nil {
			return nil, err
		}

		name, ok := nameVal.(Symbol)
		if !ok {
			return nil, fmt.Errorf("%w: object ivar name is not a symbol, got %T", ErrInvalid, nameVal)
		}

		val, err := d.readValue()
		if err != nil {
			return nil, err
		}

		obj.IVars = append(obj.IVars, IVar{Name: strings.TrimPrefix(string(name), "@"), Value: val})
	}

	d.objects[idx] = obj

	return obj, nil
}

func (d *decoder) readUserMarshalTagged() (any, error) {
	idx := d.reserve()

	class, err := d.readClassName()
	if err != nil {
		return nil, err
	}

	payload, err := d.readValue()
	if err != nil {
		return nil, err
	}

	um := &UserMarshal{Class: class, Payload: payload}
	d.objects[idx] = um

	return um, nil
}

func (d *decoder) readUserDefinedTagged() (any, error) {
	idx := d.reserve()

	class, err := d.readClassName()
	if err != nil {
		return nil, err
	}

	n, err := d.readLong()
	if err != nil {
		return nil, err
	}

	if err := checkCount(n); err != nil {
		return nil, err
	}

	data, err := d.readBytes(n)
	if err != nil {
		return nil, err
	}

	ud := &UserDefined{Class: class, Data: append([]byte(nil), data...)}
	d.objects[idx] = ud

	return ud, nil
}

func (d *decoder) readObjectLink() (any, error) {
	idx, err := d.readLong()
	if err != nil {
		return nil, err
	}

	if idx < 0 || int(idx) >= len(d.objects) {
		return nil, fmt.Errorf("%w: bad object link %d", ErrInvalid, idx)
	}

	return d.objects[idx], nil
}

// readBignum decodes Marshal's Bignum ('l') representation: a sign byte
// followed by a count of little-endian 16-bit words. Only used on decode —
// every field this package encodes fits in an int64.
func (d *decoder) readBignum() (any, error) {
	idx := d.reserve()

	signByte, err := d.readByte()
	if err != nil {
		return nil, err
	}

	size, err := d.readLong()
	if err != nil {
		return nil, err
	}

	if err := checkCount(size); err != nil {
		return nil, err
	}

	words, err := d.readBytes(size * 2)
	if err != nil {
		return nil, err
	}

	mag := make([]byte, len(words))
	for i, b := range words {
		mag[len(words)-1-i] = b
	}

	n := new(big.Int).SetBytes(mag)
	if signByte == '-' {
		n.Neg(n)
	}

	d.objects[idx] = n

	return n, nil
}
