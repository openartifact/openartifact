// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubymarshal

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestGemSpecificationRoundTrip(t *testing.T) {
	spec := Specification{
		Name:        "rack",
		Version:     "2.2.8",
		Platform:    "ruby",
		Summary:     "a modular Ruby webserver interface",
		Description: "Rack provides a minimal interface for webservers.",
		Homepage:    "https://github.com/rack/rack",
		Email:       "team@rack.dev",
		Authors:     []string{"Leah Neukirchen", "Aaron Patterson"},
		Licenses:    []string{"MIT"},
		Dependencies: []Dependency{
			{Name: "rexml", Requirement: ">= 3.0, < 4.0", Type: "runtime"},
			{Name: "minitest", Requirement: "~> 5.0", Type: "development"},
			{Name: "no-constraint", Requirement: "", Type: ""}, // exercises the default ">= 0"
		},
	}

	data, err := EncodeSpecification(spec)
	if err != nil {
		t.Fatalf("EncodeSpecification: %v", err)
	}

	// The payload must be a zlib-deflatable Marshal 4.8 UserDefined wrapper
	// (what handleGemspec serves): starts with the version header.
	if len(data) < 2 || data[0] != 4 || data[1] != 8 {
		t.Fatalf("EncodeSpecification did not produce a Marshal 4.8 stream: %x", data[:min(len(data), 8)])
	}

	got, err := DecodeSpecification(data)
	if err != nil {
		t.Fatalf("DecodeSpecification: %v", err)
	}

	if got.Name != spec.Name {
		t.Errorf("Name = %q, want %q", got.Name, spec.Name)
	}

	if got.Version != spec.Version {
		t.Errorf("Version = %q, want %q", got.Version, spec.Version)
	}

	if got.Platform != spec.Platform {
		t.Errorf("Platform = %q, want %q", got.Platform, spec.Platform)
	}

	if got.Summary != spec.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, spec.Summary)
	}

	if got.Description != spec.Description {
		t.Errorf("Description = %q, want %q", got.Description, spec.Description)
	}

	if got.Homepage != spec.Homepage {
		t.Errorf("Homepage = %q, want %q", got.Homepage, spec.Homepage)
	}

	if !reflect.DeepEqual(got.Authors, spec.Authors) {
		t.Errorf("Authors = %#v, want %#v", got.Authors, spec.Authors)
	}

	if !reflect.DeepEqual(got.Licenses, spec.Licenses) {
		t.Errorf("Licenses = %#v, want %#v", got.Licenses, spec.Licenses)
	}

	wantDeps := []Dependency{
		{Name: "rexml", Requirement: ">= 3.0, < 4.0", Type: "runtime"},
		{Name: "minitest", Requirement: "~> 5.0", Type: "development"},
		{Name: "no-constraint", Requirement: ">= 0", Type: "runtime"},
	}
	if !reflect.DeepEqual(got.Dependencies, wantDeps) {
		t.Errorf("Dependencies = %#v, want %#v", got.Dependencies, wantDeps)
	}
}

// The encoder writes a requirement of any length, the decoder reads up to
// MaxRequirementClauses clauses and refuses more: the round trip holds at the
// cap and the refusal is ErrInvalid just past it (the nightly fuzz run of
// FuzzMarshalRoundTrip reached this with a requirement of 103 clauses).
func TestSpecificationRequirementClauseCap(t *testing.T) {
	for _, clauses := range []int{1, MaxRequirementClauses, MaxRequirementClauses + 1, 2 * MaxRequirementClauses} {
		requirement := strings.TrimSuffix(strings.Repeat(">= 1, ", clauses), ", ")
		spec := Specification{
			Name: "x", Version: "1",
			Dependencies: []Dependency{{Name: "d", Requirement: requirement, Type: "runtime"}},
		}

		data, err := EncodeSpecification(spec)
		if err != nil {
			t.Fatalf("%d clauses: EncodeSpecification: %v", clauses, err)
		}

		got, err := DecodeSpecification(data)

		switch {
		case clauses > MaxRequirementClauses && !errors.Is(err, ErrInvalid):
			t.Errorf("%d clauses: DecodeSpecification err = %v, want ErrInvalid", clauses, err)
		case clauses > MaxRequirementClauses:
		case err != nil:
			t.Errorf("%d clauses: DecodeSpecification: %v", clauses, err)
		case got.Dependencies[0].Requirement != requirement:
			t.Errorf("%d clauses: requirement = %q, want %q", clauses, got.Dependencies[0].Requirement, requirement)
		}
	}
}

func TestGemSpecificationDefaultsPlatform(t *testing.T) {
	data, err := EncodeSpecification(Specification{Name: "tiny", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("EncodeSpecification: %v", err)
	}

	got, err := DecodeSpecification(data)
	if err != nil {
		t.Fatalf("DecodeSpecification: %v", err)
	}

	if got.Platform != "ruby" {
		t.Errorf("Platform = %q, want ruby (default)", got.Platform)
	}

	if len(got.Dependencies) != 0 {
		t.Errorf("Dependencies = %#v, want none", got.Dependencies)
	}
}

func TestDecodeSpecificationRejectsWrongClass(t *testing.T) {
	data, err := Encode(UserDefined{Class: "SomethingElse", Data: []byte{4, 8, '0'}})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if _, err := DecodeSpecification(data); err == nil {
		t.Error("expected DecodeSpecification to reject a non-Gem::Specification payload")
	}
}

// TestDecodeSpecificationRejectsHostileNestedLength simulates the exact
// attack surface RubyGemsHandler.Parse exposes: an uploaded .gem's
// metadata.gz decompresses to attacker-controlled bytes that get handed to
// DecodeSpecification verbatim. Here the inner (_dump payload) stream
// itself is a hand-built Marshal array claiming a ~2^31 element count —
// DecodeSpecification must return an error, never panic or attempt the
// multi-GB allocation that count would otherwise trigger.
func TestDecodeSpecificationRejectsHostileNestedLength(t *testing.T) {
	hostile := []byte{4, 8, '[', 4, 0xFF, 0xFF, 0xFF, 0x7F} // claims a ~2^31-element array

	data, err := Encode(UserDefined{Class: "Gem::Specification", Data: hostile})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if _, err := DecodeSpecification(data); err == nil {
		t.Error("expected DecodeSpecification to reject a hostile huge nested array length")
	}
}
