// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpgkeys

import (
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/formats"
)

// TestMain registers the formats: a key change refreshes through their
// lifecycle hooks.
func TestMain(m *testing.M) {
	formats.Register()

	os.Exit(m.Run())
}
