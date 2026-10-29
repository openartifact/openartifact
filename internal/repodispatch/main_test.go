// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/formats"
)

// TestMain registers the formats, as the server does: the dispatcher reads the
// proxy and group hooks they register.
func TestMain(m *testing.M) {
	formats.Register()

	os.Exit(m.Run())
}
