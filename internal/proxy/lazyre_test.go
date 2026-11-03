// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"testing"

	"github.com/openartifact/openartifact/internal/lazyre"
)

// TestLazyExpressionsCompile compiles every expression the package declares
// with lazyre.New, which would otherwise fail at its first use in a request.
func TestLazyExpressionsCompile(t *testing.T) {
	t.Parallel()

	if err := lazyre.Check(); err != nil {
		t.Fatal(err)
	}
}
