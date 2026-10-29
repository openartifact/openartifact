// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/formats"
)

// TestMain registers the formats: the analysis workers read the manifest and
// dependency hooks they provide.
func TestMain(m *testing.M) {
	formats.Register()

	os.Exit(m.Run())
}
