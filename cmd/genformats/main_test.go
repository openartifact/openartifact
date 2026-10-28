// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommittedFileIsCurrent is the drift check of the generated web
// constants: it fails until `make generate` output is committed.
func TestCommittedFileIsCurrent(t *testing.T) {
	t.Parallel()

	if err := run(filepath.Join("..", ".."), true); err != nil {
		t.Fatal(err)
	}
}

func TestRenderCoversPolicy(t *testing.T) {
	t.Parallel()

	out := string(render())

	for _, want := range []string{
		"export const FORMAT_IDS = ['ansible', 'apk',",
		"export type FormatId = (typeof FORMAT_IDS)[number]",
		"export const FEDERATION_FORMATS: readonly FormatId[] = ['cargo', 'docker', 'go', 'helm', 'maven', 'npm', 'nuget', 'pypi', 'raw']",
		"export const ISOLATED_FORMATS: readonly FormatId[] = ['ivy', 'maven', 'raw', 'sbt']",
		"  docker: 'https://registry-1.docker.io',",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated file lacks %q", want)
		}
	}
}

func TestWriteThenCheck(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web", "src", "lib"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := run(root, true); err == nil {
		t.Fatal("check passed without a generated file")
	}

	if err := run(root, false); err != nil {
		t.Fatal(err)
	}

	if err := run(root, true); err != nil {
		t.Fatalf("check after write: %v", err)
	}

	path := filepath.Join(root, filepath.FromSlash(outFile))
	if err := os.WriteFile(path, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run(root, true); err == nil {
		t.Fatal("check passed on a stale file")
	}
}
