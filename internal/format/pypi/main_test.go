// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/licensemeta"
)

// TestMain registers the manifest rule and the license hooks of the format, the
// way internal/formats does at startup: the handler reads the license of an
// upload through licensemeta, which dispatches by format. The package cannot
// import internal/formats, which imports it.
func TestMain(m *testing.M) {
	manifest.Register("pypi", manifest.PyPIRule)
	licensemeta.Register("pypi", licensemeta.PyPIHooks)

	os.Exit(m.Run())
}
