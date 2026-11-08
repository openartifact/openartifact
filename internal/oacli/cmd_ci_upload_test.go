// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"os"
	"testing"
)

func TestGlobFilesAcceptsDotSlashPatterns(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.MkdirAll("dist", 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile("dist/x.jar", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, pat := range []string{"dist/*.jar", "./dist/*.jar", "./dist/**"} {
		got, err := globFiles(pat)
		if err != nil || len(got) != 1 || got[0] != "dist/x.jar" {
			t.Errorf("globFiles(%q) = %v, %v; want [dist/x.jar]", pat, got, err)
		}
	}
}
