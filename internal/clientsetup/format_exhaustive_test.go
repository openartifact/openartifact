// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package clientsetup

import (
	"testing"

	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestBuilderForCoversEveryFormat: a format without a builder would show the
// bare-URL fallback instead of real client setup instructions.
func TestBuilderForCoversEveryFormat(t *testing.T) {
	t.Parallel()

	repotypetest.Classified(t, "clientsetup.builderFor", repotype.Names(), nil)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		if builderFor(format) == nil {
			t.Fatalf("builderFor(%q) = nil", format)
		}

		for _, repoType := range []string{repotype.Hosted, repotype.Proxy, repotype.Group} {
			snippets := Snippets(format, "https://oa.example.com", "my-repo", repoType)
			if len(snippets) == 0 {
				t.Fatalf("Snippets(%q, %q) is empty", format, repoType)
			}

			for _, s := range snippets {
				if s.Client == "" || s.Client == "URL" || s.Configure == "" {
					t.Errorf("Snippets(%q, %q) has a bare or incomplete entry: %+v", format, repoType, s)
				}
			}
		}
	})
}
