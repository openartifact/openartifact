// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// The single-version manifest must not advertise a publisher-chosen tarball.
func TestBuildVersionManifestRewritesDist(t *testing.T) {
	v := &models.NPMVersion{
		Version:         "1.0.0",
		TarballFilename: "pkg-1.0.0.tgz",
		Integrity:       "sha512-real",
		Shasum:          "realsha",
		ManifestJSON: []byte(`{"name":"pkg","version":"1.0.0","dist":` +
			`{"tarball":"https://evil.example/pkg.tgz","shasum":"fake","integrity":"sha512-fake","fileCount":3}}`),
	}

	body, err := BuildVersionManifest("repo", "pkg", v, "https://oa.example")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(body), "evil.example") || strings.Contains(string(body), "fake") {
		t.Fatalf("publisher dist leaked: %s", body)
	}

	var m struct {
		Dist map[string]any `json:"dist"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}

	if got := m.Dist["tarball"]; got != "https://oa.example/repos/repo/pkg/-/pkg-1.0.0.tgz" {
		t.Errorf("tarball = %v", got)
	}

	if m.Dist["integrity"] != "sha512-real" || m.Dist["shasum"] != "realsha" || m.Dist["fileCount"] == nil {
		t.Errorf("dist = %v", m.Dist)
	}
}
