// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubymarshal

import (
	"bytes"
	"testing"
)

// FuzzDecode feeds arbitrary bytes to the Marshal decoder and the gemspec
// decoder on top of it (the entry point for an uploaded gem's untrusted
// metadata.gz): both must return a value or an error, never panic, recurse
// without bound or allocate without bound.
func FuzzDecode(f *testing.F) {
	for _, v := range []any{
		nil, true, int64(0), int64(-129), int64(1 << 40), 1.5, "text", Symbol("sym"),
		[]any{int64(1), "two", Symbol("three"), []any{}},
		Hash{{Key: Symbol("k"), Value: "v"}},
		&Object{Class: "Gem::Dependency", IVars: []IVar{{Name: "@name", Value: "rake"}}},
		UserDefined{Class: "Gem::Specification", Data: []byte("data")},
		NewGemVersion("1.2.3"),
	} {
		if data, err := Encode(v); err == nil {
			f.Add(data)
		}
	}

	spec, err := EncodeSpecification(Specification{
		Name: "fuzz", Version: "1.0.0", Summary: "s", Authors: []string{"a"},
		Dependencies: []Dependency{{Name: "rake", Requirement: ">= 1.0", Type: "runtime"}},
	})
	if err == nil {
		f.Add(spec)
	}

	// Hostile shapes: unbounded nesting, hostile lengths, truncation.
	f.Add(append([]byte{4, 8}, bytes.Repeat([]byte{'I'}, 4096)...))
	f.Add(append([]byte{4, 8}, bytes.Repeat([]byte{'[', 0x06}, 4096)...))
	f.Add([]byte{4, 8, '[', 4, 0xFF, 0xFF, 0xFF, 0x7F})
	f.Add([]byte{4, 8, '"', 0x80})
	f.Add([]byte{4, 8})
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = Decode(data)
		_, _ = DecodeSpecification(data)
	})
}
