// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubymarshal

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// linkedGemspec hand-builds a Gem::Specification whose dependency list is one
// real Gem::Dependency followed by depLinks '@' links to it, where that
// dependency's Gem::Requirement holds one real clause followed by clauseLinks
// '@' links to the same clause. As a DAG it is a few bytes per link; walked as
// a tree it is depLinks*clauseLinks clauses.
func linkedGemspec(t *testing.T, depLinks, clauseLinks int) []byte {
	t.Helper()

	const (
		depIdx    = 2 // object table slots, in decode order: array, deps, dep
		clauseIdx = 7 // array, deps, dep, name, req, payload, clauses, clause
	)

	e := &encoder{buf: []byte{majorVersion, minorVersion}}
	nils := func(n int) { e.buf = append(e.buf, strings.Repeat("0", n)...) }
	link := func(idx, count int) {
		for range count {
			e.buf = append(e.buf, '@')
			e.writeLong(int64(idx))
		}
	}

	e.buf = append(e.buf, '[')
	e.writeLong(19)
	nils(9)

	e.buf = append(e.buf, '[') // dependencies
	e.writeLong(int64(1 + depLinks))
	e.buf = append(e.buf, 'o')
	e.writeSymbol("Gem::Dependency")
	e.writeLong(2)
	e.writeSymbol("@name")
	e.writeValue("dep")
	e.writeSymbol("@requirement")
	e.buf = append(e.buf, 'U')
	e.writeSymbol("Gem::Requirement")
	e.buf = append(e.buf, '[')
	e.writeLong(1)
	e.buf = append(e.buf, '[') // clauses
	e.writeLong(int64(1 + clauseLinks))
	e.writeValue([]any{">=", NewGemVersion("1")})
	link(clauseIdx, clauseLinks)
	link(depIdx, depLinks)

	nils(9)

	return mustEncodeUserDefined(t, "Gem::Specification", e.buf)
}

func mustEncodeUserDefined(t *testing.T, class string, inner []byte) []byte {
	t.Helper()

	data, err := Encode(UserDefined{Class: class, Data: inner})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	return data
}

// TestDecodeSpecificationRejectsLinkAmplification: Marshal '@' links hand back
// the SAME decoded value, so a tiny stream can describe a huge tree. The
// consumers (decodeRequirement, storableText, JSON marshalling) walk the graph
// as a tree, so DecodeSpecification must bound the tree it materialises.
func TestDecodeSpecificationRejectsLinkAmplification(t *testing.T) {
	big := strings.Repeat("a", 600<<10)

	cases := map[string][]byte{
		"too many clauses in one requirement": linkedGemspec(t, 0, 1000),
		"many deps sharing one requirement":   linkedGemspec(t, maxSpecDependencies-1, maxSpecClauses-1),
		"too many dependencies":               linkedGemspec(t, maxSpecDependencies+1, 0),
		"authors linking one large string": func() []byte {
			spec, err := EncodeSpecification(Specification{Name: "x", Version: "1", Authors: []string{big, big}})
			if err != nil {
				t.Fatalf("EncodeSpecification: %v", err)
			}

			return spec
		}(),
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSpecification(data); !errors.Is(err, ErrInvalid) {
				t.Fatalf("DecodeSpecification: err = %v, want ErrInvalid", err)
			}
		})
	}
}

// TestDecodeSpecificationAcceptsLargeRealisticSpec guards the limits against
// rejecting a big-but-legitimate gemspec.
func TestDecodeSpecificationAcceptsLargeRealisticSpec(t *testing.T) {
	spec := Specification{Name: "big", Version: "1.0.0", Description: strings.Repeat("d", 64<<10)}
	for i := range 500 {
		spec.Dependencies = append(spec.Dependencies, Dependency{
			Name:        fmt.Sprintf("dependency-%d", i),
			Requirement: ">= 1.0, < 3.0",
			Type:        "runtime",
		})
	}

	data, err := EncodeSpecification(spec)
	if err != nil {
		t.Fatalf("EncodeSpecification: %v", err)
	}

	got, err := DecodeSpecification(data)
	if err != nil {
		t.Fatalf("DecodeSpecification: %v", err)
	}

	if len(got.Dependencies) != 500 || got.Dependencies[499].Requirement != ">= 1.0, < 3.0" {
		t.Fatalf("dependencies = %d, last = %+v", len(got.Dependencies), got.Dependencies[len(got.Dependencies)-1])
	}
}
