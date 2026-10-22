// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubymarshal

import (
	"bytes"
	"errors"
	"testing"
)

// wantDepthLimit is deliberately a literal rather than maxMarshalDepth, so the
// boundary tests keep their meaning if the constant is ever changed (and
// TestDepthLimitConstant makes that change a conscious one).
const wantDepthLimit = 64

func TestDepthLimitConstant(t *testing.T) {
	if maxMarshalDepth != wantDepthLimit {
		t.Fatalf("maxMarshalDepth = %d, want %d: update the depth tests together with the limit", maxMarshalDepth, wantDepthLimit)
	}
}

// nestingShape builds a Marshal 4.8 stream in which a nil leaf is wrapped by
// levels containers. The decoder therefore performs levels+1 readValue calls.
type nestingShape struct {
	name  string
	build func(levels int) []byte
}

func nestingShapes() []nestingShape {
	header := []byte{4, 8}

	return []nestingShape{
		{"ivar wrapper", func(n int) []byte {
			// 'I' <value> <ivar count>: the zero counts close each level.
			b := append([]byte{}, header...)
			b = append(b, bytes.Repeat([]byte{'I'}, n)...)
			b = append(b, '0')

			return append(b, bytes.Repeat([]byte{0}, n)...)
		}},
		{"array", func(n int) []byte {
			b := append([]byte{}, header...)
			b = append(b, bytes.Repeat([]byte{'[', 0x06}, n)...)

			return append(b, '0')
		}},
		{"hash value", func(n int) []byte {
			// '{' count=1 key=nil value=<next level>
			b := append([]byte{}, header...)
			b = append(b, bytes.Repeat([]byte{'{', 0x06, '0'}, n)...)

			return append(b, '0')
		}},
		{"object ivar", func(n int) []byte {
			// 'o' :A, one ivar: key = symbol link 0, value = <next level>
			level := []byte{'o', ':', 0x06, 'A', 0x06, ';', 0}
			b := append([]byte{}, header...)
			b = append(b, level...)

			for range n - 1 {
				b = append(b, 'o', ';', 0, 0x06, ';', 0)
			}

			return append(b, '0')
		}},
		{"user marshal", func(n int) []byte {
			b := append([]byte{}, header...)
			b = append(b, 'U', ':', 0x06, 'A')

			for range n - 1 {
				b = append(b, 'U', ';', 0)
			}

			return append(b, '0')
		}},
	}
}

// TestDecodeAcceptsNestingUpToLimit guards against the limit being so tight
// that it breaks real payloads.
func TestDecodeAcceptsNestingUpToLimit(t *testing.T) {
	for _, shape := range nestingShapes() {
		t.Run(shape.name, func(t *testing.T) {
			// levels containers around a nil leaf = levels+1 nested values.
			data := shape.build(wantDepthLimit - 1)

			if _, err := Decode(data); err != nil {
				t.Fatalf("Decode at the depth limit: %v", err)
			}
		})
	}
}

func TestDecodeRejectsNestingPastLimit(t *testing.T) {
	for _, shape := range nestingShapes() {
		t.Run(shape.name, func(t *testing.T) {
			for _, levels := range []int{wantDepthLimit, wantDepthLimit * 4, 100_000} {
				_, err := Decode(shape.build(levels))
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("Decode with %d levels: err = %v, want ErrInvalid", levels, err)
				}
			}
		})
	}
}

// TestDecodeSpecificationRejectsDeepNestedPayload covers the second Decode in
// DecodeSpecification: the Gem::Specification's inner Data is itself a
// Marshal stream and must be depth-limited as well.
func TestDecodeSpecificationRejectsDeepNestedPayload(t *testing.T) {
	inner := nestingShapes()[1].build(100_000)

	data, err := Encode(UserDefined{Class: "Gem::Specification", Data: inner})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if _, err := DecodeSpecification(data); !errors.Is(err, ErrInvalid) {
		t.Fatalf("DecodeSpecification: err = %v, want ErrInvalid", err)
	}
}

// TestDecodeSurvivesHugeNesting is the reported attack: a Marshal stream of
// 4 MiB of 'I' bytes (the metadata.gz size cap) nests four million levels
// deep. Before the depth limit this overflowed the goroutine stack, a fatal
// error that cannot be recovered and takes the whole server down.
func TestDecodeSurvivesHugeNesting(t *testing.T) {
	data := append([]byte{4, 8}, bytes.Repeat([]byte{'I'}, 4<<20)...)

	_, err := Decode(data)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Decode(4 MiB of 'I'): err = %v, want ErrInvalid", err)
	}
}
