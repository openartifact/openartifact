// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package registry_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/registry"
)

func TestRegistry(t *testing.T) {
	t.Parallel()

	var r registry.Registry[int]

	_, ok := r.Lookup("a")
	assert.False(t, ok)
	assert.Empty(t, r.Names())

	r.Register("b", 2)
	r.Register("a", 1)

	v, ok := r.Lookup("a")
	require.True(t, ok)
	assert.Equal(t, 1, v)
	assert.Equal(t, []string{"a", "b"}, r.Names())

	assert.Panics(t, func() { r.Register("a", 3) })
	assert.Panics(t, func() { r.Register("", 3) })
}

func TestNamesIsSortedAndFollowsRegister(t *testing.T) {
	t.Parallel()

	var r registry.Registry[int]

	for _, name := range []string{"m", "c", "x", "a"} {
		r.Register(name, 1)
		assert.IsIncreasing(t, r.Names(), "Names must stay sorted after registering %q", name)
	}

	before := r.Names()
	assert.Equal(t, []string{"a", "c", "m", "x"}, before)

	r.Register("b", 2)

	assert.Equal(t, []string{"a", "b", "c", "m", "x"}, r.Names(), "a later Register must show up")
	assert.Equal(t, []string{"a", "c", "m", "x"}, before, "a slice handed out earlier is a snapshot, not rewritten by Register")
}

// Not parallel: testing.AllocsPerRun panics inside a parallel test.
func TestNamesIsComputedOncePerRegistration(t *testing.T) {
	var r registry.Registry[int]

	r.Register("a", 1)
	r.Register("b", 2)

	first, second := r.Names(), r.Names()
	require.Len(t, first, 2)
	assert.Same(t, &first[0], &second[0], "Names must hand out the cached slice, not build one per call")
	assert.Zero(t, testing.AllocsPerRun(100, func() { _ = r.Names() }))

	// An append by a caller must reallocate rather than write into the slack of
	// the shared array (two callers appending would race).
	assert.Equal(t, len(first), cap(first))

	grown := append(first, "z")
	require.Len(t, grown, 3)
	assert.Equal(t, []string{"a", "b"}, r.Names())
}

type bigValue struct {
	payload [4096]byte
	tag     string
}

// Not parallel: testing.AllocsPerRun panics inside a parallel test.
func TestLookupRefSharesTheRegisteredValue(t *testing.T) {
	var r registry.Registry[bigValue]

	_, ok := r.LookupRef("a")
	assert.False(t, ok)

	r.Register("a", bigValue{payload: [4096]byte{0: 7}, tag: "first"})

	ref, ok := r.LookupRef("a")
	require.True(t, ok)
	assert.Equal(t, "first", ref.tag)
	assert.Equal(t, byte(7), ref.payload[0])

	again, _ := r.LookupRef("a")
	assert.Same(t, ref, again, "LookupRef must not copy the value")
	assert.Zero(t, testing.AllocsPerRun(100, func() { _, _ = r.LookupRef("a") }))

	v, ok := r.Lookup("a")
	require.True(t, ok)
	assert.Equal(t, "first", v.tag)

	v.tag = "changed"
	assert.Equal(t, "first", ref.tag, "Lookup returns a copy: editing it must not reach the registry")
}

func TestRegisterIsSafeAgainstConcurrentReaders(t *testing.T) {
	t.Parallel()

	var (
		r  registry.Registry[int]
		wg sync.WaitGroup
	)

	const writers, readers = 8, 8

	for i := range writers {
		wg.Go(func() { r.Register(fmt.Sprintf("n%02d", i), i) })
	}

	for range readers {
		wg.Go(func() {
			for range 200 {
				assert.IsIncreasing(t, r.Names())
			}
		})
	}

	wg.Wait()

	assert.Len(t, r.Names(), writers)
}
