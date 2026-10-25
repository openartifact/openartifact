// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package clientsetup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// parityFixture is the golden file shared with the web frontend: the bun test
// web/src/lib/__tests__/installCommands.parity.test.ts feeds the same inputs to
// clientSetup/repoBaseUrl in web/src/lib/installCommands.ts and expects the
// same output. Run `UPDATE_PARITY=1 go test ./internal/clientsetup -run Parity`
// after an intentional change here, then update installCommands.ts to match.
const parityFixture = "testdata/web_parity.json"

type parityCase struct {
	Format   string    `json:"format"`
	Origin   string    `json:"origin"`
	Repo     string    `json:"repo"`
	BaseURL  string    `json:"base_url"`
	Snippets []Snippet `json:"snippets"`
}

func TestWebParityFixture(t *testing.T) {
	var cases []parityCase

	for _, format := range allFormats() {
		for _, repo := range []string{"repo1", "my repo"} {
			cases = append(cases, parityCase{
				Format:   format,
				Origin:   "https://artifacts.example.com:8443",
				Repo:     repo,
				BaseURL:  RepoBaseURL(format, "https://artifacts.example.com:8443", repo),
				Snippets: Snippets(format, "https://artifacts.example.com:8443", repo),
			})
		}
	}

	got, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	got = append(got, '\n')

	if os.Getenv("UPDATE_PARITY") != "" {
		if err := os.WriteFile(filepath.FromSlash(parityFixture), got, 0o600); err != nil {
			t.Fatal(err)
		}

		return
	}

	want, err := os.ReadFile(filepath.FromSlash(parityFixture))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: rerun with UPDATE_PARITY=1 and update web/src/lib/installCommands.ts to match", parityFixture)
	}
}
