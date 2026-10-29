// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package registry_test

import (
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
