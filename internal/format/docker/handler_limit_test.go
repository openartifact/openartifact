// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"strings"
	"testing"
)

func TestDockerHandler_OversizedManifestRejected(t *testing.T) {
	h := NewDockerHandler()
	huge := `{"schemaVersion":2,"x":"` + strings.Repeat("a", maxManifestBytes) + `"}`

	if err := h.Validate(context.Background(), strings.NewReader(huge)); err == nil {
		t.Error("Validate accepted an oversized manifest")
	}

	if _, err := h.Parse(context.Background(), strings.NewReader(huge)); err == nil {
		t.Error("Parse accepted an oversized manifest")
	}
}
