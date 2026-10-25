// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"
)

func createGoModZip(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}

		_, _ = w.Write([]byte(content))
	}

	_ = zw.Close()

	return buf.Bytes()
}

const sampleGoMod = `module github.com/example/mymodule

go 1.21

require (
	github.com/gorilla/mux v1.8.0
)
`

func parseModuleZip(data []byte) (*moduleFile, error) {
	return parseModuleZipAt(bytes.NewReader(data), int64(len(data)))
}

func TestParseModuleZip(t *testing.T) {
	const prefix = "github.com/foo/bar@v1.2.3/"

	cases := []struct {
		name       string
		files      map[string]string
		wantEntry  string
		wantModule string
		wantErr    error
	}{
		{
			name:       "plain module line",
			files:      map[string]string{prefix + "go.mod": "module github.com/foo/bar\ngo 1.21\n"},
			wantEntry:  prefix + "go.mod",
			wantModule: "github.com/foo/bar",
		},
		{
			name:       "extra spaces and a trailing comment",
			files:      map[string]string{prefix + "go.mod": "  module   github.com/foo/bar  // legacy\n"},
			wantEntry:  prefix + "go.mod",
			wantModule: "github.com/foo/bar",
		},
		{
			name:       "quoted module path",
			files:      map[string]string{prefix + "go.mod": "module \"github.com/foo/bar\"\n"},
			wantEntry:  prefix + "go.mod",
			wantModule: "github.com/foo/bar",
		},
		{
			name:    "no module line",
			files:   map[string]string{prefix + "go.mod": "go 1.21\nno module line\n"},
			wantErr: errNoModuleLine,
		},
		{
			name:    "foo-go.mod is not a go.mod",
			files:   map[string]string{prefix + "foo-go.mod": "module github.com/foo/bar\n"},
			wantErr: errNoGoMod,
		},
		{
			name: "root go.mod wins over a nested one",
			files: map[string]string{
				prefix + "nested/go.mod": "module github.com/foo/nested\n",
				prefix + "go.mod":        "module github.com/foo/bar\n",
			},
			wantEntry:  prefix + "go.mod",
			wantModule: "github.com/foo/bar",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseModuleZip(createGoModZip(t, c.files))
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.entry != c.wantEntry || got.modulePath != c.wantModule {
				t.Errorf("got entry %q module %q, want %q %q", got.entry, got.modulePath, c.wantEntry, c.wantModule)
			}
		})
	}
}
