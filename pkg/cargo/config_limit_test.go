// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"strings"
	"testing"
)

func TestParseConfig_ReadIsCapped(t *testing.T) {
	// The dl key sits past the 1 MiB cap, so a capped read cannot see it.
	doc := `{"pad":"` + strings.Repeat("a", 2<<20) + `","dl":"https://x/{crate}"}`

	if _, err := ParseConfig(strings.NewReader(doc)); err == nil {
		t.Fatal("expected an oversized config.json to be rejected")
	}
}
