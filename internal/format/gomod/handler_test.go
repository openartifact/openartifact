// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
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

func TestGoModHandler_Meta(t *testing.T) {
	h := NewGoModHandler()
	if h.Name() != "go" {
		t.Errorf("Name() = %q, want go", h.Name())
	}

	if h.GetContentType() != "application/zip" {
		t.Errorf("GetContentType() = %q, want application/zip", h.GetContentType())
	}

	if h.GetFileExtension() != ".zip" {
		t.Errorf("GetFileExtension() = %q, want .zip", h.GetFileExtension())
	}
}

func TestGoModHandler_Validate(t *testing.T) {
	h := NewGoModHandler()
	ctx := context.Background()

	t.Run("valid zip with go.mod", func(t *testing.T) {
		data := createGoModZip(t, map[string]string{"github.com/example/mymodule@v1.0.0/go.mod": sampleGoMod})
		if err := h.Validate(ctx, bytes.NewReader(data)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid: only a file ending in go.mod", func(t *testing.T) {
		data := createGoModZip(t, map[string]string{"github.com/example/mymodule@v1.0.0/foo-go.mod": sampleGoMod})
		if err := h.Validate(ctx, bytes.NewReader(data)); err == nil {
			t.Error("foo-go.mod must not pass for go.mod")
		}
	})

	t.Run("invalid: not a zip", func(t *testing.T) {
		if err := h.Validate(ctx, strings.NewReader("not a zip")); err == nil {
			t.Error("expected error for non-zip")
		}
	})

	t.Run("invalid: zip without go.mod", func(t *testing.T) {
		data := createGoModZip(t, map[string]string{"somefile.txt": "hello"})
		if err := h.Validate(ctx, bytes.NewReader(data)); err == nil {
			t.Error("expected error for zip without go.mod")
		}
	})
}

func TestGoModHandler_Parse(t *testing.T) {
	h := NewGoModHandler()
	ctx := context.Background()

	t.Run("valid module", func(t *testing.T) {
		data := createGoModZip(t, map[string]string{"github.com/example/mymodule@v1.0.0/go.mod": sampleGoMod})

		result, err := h.Parse(ctx, bytes.NewReader(data))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result["module_path"] != "github.com/example/mymodule" {
			t.Errorf("module_path = %v, want github.com/example/mymodule", result["module_path"])
		}

		if _, ok := result["go_mod"]; !ok {
			t.Error("missing go_mod in result")
		}
	})

	t.Run("invalid: not a zip", func(t *testing.T) {
		_, err := h.Parse(ctx, strings.NewReader("garbage"))
		if err == nil {
			t.Error("expected error")
		}
	})
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
