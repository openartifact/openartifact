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

func TestRenderClientSetup(t *testing.T) {
	t.Parallel()

	out := string(renderClientSetup())

	for _, want := range []string{
		"export const DEFAULT_BASE_TEMPLATE = '{{origin}}/repos/{{esc}}'",
		"  docker: '{{origin}}',",
		"  maven: '{{origin}}/repos/maven/{{esc}}',",
		"  npm: {\n    hosted: [",
		"    proxy: [", // apk, nix and pacman differ per repository type
		"{{#http}}\\ncomposer config secure-http false{{/http}}",
		"<TOKEN>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated client setup lacks %q", want)
		}
	}

	// npm snippets do not vary by repository type.
	_, npm, _ := strings.Cut(out, "\n  npm: {")
	npm, _, _ = strings.Cut(npm, "\n  },")

	if strings.Contains(npm, "proxy:") || strings.Contains(npm, "group:") {
		t.Errorf("npm lists a proxy or group variant identical to hosted:\n%s", npm)
	}
}

func TestTSString(t *testing.T) {
	t.Parallel()

	if got, want := tsString("it's a\\b\nc\t\u2028"), `'it\'s a\\b\nc\t\u2028'`; got != want {
		t.Errorf("tsString = %s, want %s", got, want)
	}
}
