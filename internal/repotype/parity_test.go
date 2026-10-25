// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repotype

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// parityFixture is the golden file shared with the web frontend: the bun test
// web/src/lib/__tests__/repoTypes.parity.test.ts checks web/src/lib/repoTypes.ts
// against it. Run `UPDATE_PARITY=1 go test ./internal/repotype -run Parity`
// after an intentional policy change, then update repoTypes.ts to match.
const parityFixture = "testdata/web_parity.json"

type parityFormat struct {
	Format          string `json:"format"`
	Proxy           bool   `json:"proxy"`
	Group           bool   `json:"group"`
	Federation      bool   `json:"federation"`
	DefaultUpstream string `json:"default_upstream"`
}

func TestWebParityFixture(t *testing.T) {
	formats := []string{
		"docker", "npm", "pypi", "maven", "helm", "cargo", "rubygems", "deb",
		"rpm", "nuget", "go", "raw", "conda", "conan", "ivy", "sbt",
	}

	out := make([]parityFormat, 0, len(formats))
	for _, f := range formats {
		out = append(out, parityFormat{
			Format:          f,
			Proxy:           ProxySupported(f),
			Group:           GroupSupported(f),
			Federation:      FederationSupported(f),
			DefaultUpstream: DefaultUpstreamURL(f),
		})
	}

	got, err := json.MarshalIndent(out, "", "  ")
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
		t.Fatalf("%s is stale: rerun with UPDATE_PARITY=1 and update web/src/lib/repoTypes.ts to match", parityFixture)
	}
}
