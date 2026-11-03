// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeSpecFixture(t *testing.T, body string) string {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "api"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, apiSpecFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	return root
}

func TestSyncSpecGzip(t *testing.T) {
	t.Parallel()

	spec := `{"openapi":"3.1.1","paths":{}}`
	root := writeSpecFixture(t, spec)

	if stale, err := syncSpecGzip(root, false); err != nil || !stale {
		t.Fatalf("missing gzip: stale = %v, err = %v; want stale", stale, err)
	}

	if _, err := os.Stat(filepath.Join(root, apiSpecGzFile)); err == nil {
		t.Fatal("check mode wrote the gzip file")
	}

	if stale, err := syncSpecGzip(root, true); err != nil || !stale {
		t.Fatalf("write: stale = %v, err = %v; want a rewrite", stale, err)
	}

	got, err := inflateFile(filepath.Join(root, apiSpecGzFile))
	if err != nil || !bytes.Equal(got, []byte(spec)) {
		t.Fatalf("inflated gzip = %q (%v), want the spec", got, err)
	}

	if stale, err := syncSpecGzip(root, false); err != nil || stale {
		t.Fatalf("current gzip: stale = %v, err = %v; want current", stale, err)
	}

	// The source changes: the gzip is stale until it is regenerated.
	if err := os.WriteFile(filepath.Join(root, apiSpecFile), []byte(`{"openapi":"3.1.1","paths":{"/x":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if stale, err := syncSpecGzip(root, false); err != nil || !stale {
		t.Fatalf("changed spec: stale = %v, err = %v; want stale", stale, err)
	}
}

func TestSyncSpecGzipReplacesCorruptFile(t *testing.T) {
	t.Parallel()

	root := writeSpecFixture(t, `{"a":1}`)
	if err := os.WriteFile(filepath.Join(root, apiSpecGzFile), []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}

	if stale, err := syncSpecGzip(root, true); err != nil || !stale {
		t.Fatalf("stale = %v, err = %v; want the corrupt file replaced", stale, err)
	}

	if stale, err := syncSpecGzip(root, false); err != nil || stale {
		t.Fatalf("after the rewrite: stale = %v, err = %v; want current", stale, err)
	}
}
