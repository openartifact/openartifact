// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
)

// seq makes the formats and kinds a test registers unique for the whole test
// binary: the registry is process-global and refuses a second registration.
var seq atomic.Int64 //nolint:gochecknoglobals // test-binary-wide uniqueness counter

func TestRegisterLookup(t *testing.T) {
	Register("cargo", Storage{})

	if _, ok := Lookup("cargo"); !ok {
		t.Fatal("cargo is registered but not found")
	}

	if _, ok := Lookup("npm"); ok {
		t.Fatal("npm was never registered")
	}

	if got := Names(); !slices.Contains(got, "cargo") {
		t.Fatalf("Names = %v, want it to hold cargo", got)
	}
}

func TestTrashFormatsOfKindFollowsRegister(t *testing.T) {
	n := seq.Add(1)
	kind := fmt.Sprintf("kindtest_%d", n)
	late, early := fmt.Sprintf("zz-late-%d", n), fmt.Sprintf("aa-early-%d", n)

	if got := TrashFormatsOfKind(kind); len(got) != 0 {
		t.Fatalf("TrashFormatsOfKind(%q) before any registration = %v, want none", kind, got)
	}

	Register(late, Storage{Trash: Trash{Kind: kind}})

	if got := TrashFormatsOfKind(kind); !slices.Equal(got, []string{late}) {
		t.Fatalf("after the first Register = %v, want [%s]", got, late)
	}

	// A format registered later with the same kind joins the set, in registry
	// (sorted) order, not registration order.
	Register(early, Storage{Trash: Trash{Kind: kind}})

	if got := TrashFormatsOfKind(kind); !slices.Equal(got, []string{early, late}) {
		t.Fatalf("after the second Register = %v, want [%s %s]", got, early, late)
	}

	// Registering a different kind leaves this one alone.
	other := fmt.Sprintf("kindother_%d", n)
	Register(fmt.Sprintf("mid-%d", n), Storage{Trash: Trash{Kind: other}})

	if got := TrashFormatsOfKind(kind); !slices.Equal(got, []string{early, late}) {
		t.Fatalf("after an unrelated Register = %v, want [%s %s]", got, early, late)
	}

	if got := TrashFormatsOfKind(other); len(got) != 1 {
		t.Fatalf("TrashFormatsOfKind(%q) = %v, want exactly the format just registered", other, got)
	}
}

func TestTrashFormatsOfKindTable(t *testing.T) {
	n := seq.Add(1)
	kind := fmt.Sprintf("kindtable_%d", n)
	withKind, withoutKind := fmt.Sprintf("with-kind-%d", n), fmt.Sprintf("no-kind-%d", n)

	Register(withKind, Storage{Trash: Trash{Kind: kind}})
	Register(withoutKind, Storage{})

	tests := []struct {
		name string
		kind string
		want []string
	}{
		{"registered kind", kind, []string{withKind}},
		{"unknown kind", "kindtable_unknown", nil},
		{"empty kind is never a trash kind", "", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrashFormatsOfKind(tc.kind); !slices.Equal(got, tc.want) {
				t.Errorf("TrashFormatsOfKind(%q) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}

	if k, ok := TrashKindOf(withKind); !ok || k != kind {
		t.Errorf("TrashKindOf(%q) = %q, %v; want %q, true", withKind, k, ok, kind)
	}

	if k, ok := TrashKindOf(withoutKind); !ok || k != "" {
		t.Errorf("TrashKindOf(%q) = %q, %v; want an empty kind of a registered format", withoutKind, k, ok)
	}

	if _, ok := TrashKindOf("never-registered"); ok {
		t.Error("TrashKindOf of an unregistered format must report false")
	}
}

func TestTrashFormatsOfKindDoesNotAllocate(t *testing.T) {
	n := seq.Add(1)
	kind := fmt.Sprintf("kindalloc_%d", n)

	Register(fmt.Sprintf("alloc-%d", n), Storage{Trash: Trash{Kind: kind}})

	if allocs := testing.AllocsPerRun(100, func() { _ = TrashFormatsOfKind(kind) }); allocs != 0 {
		t.Errorf("TrashFormatsOfKind allocates %v times per call, want 0", allocs)
	}
}
