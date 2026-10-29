// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/formats"
)

// TestMain registers the formats, as the server does: the tests read the hooks
// and tables they register.
func TestMain(m *testing.M) {
	formats.Register()

	os.Exit(m.Run())
}
