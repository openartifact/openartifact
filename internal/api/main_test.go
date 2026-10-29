// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/formats"
)

// TestMain registers the formats: the handlers read the analysis hooks and the
// metadata lifecycles they provide.
func TestMain(m *testing.M) {
	formats.Register()

	os.Exit(m.Run())
}
