// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/composer"
	"github.com/openartifact/openartifact/pkg/composer/composertest"
)

func read(t *testing.T, data []byte) (*composer.Manifest, error) {
	t.Helper()

	return composer.ReadArchive(bytes.NewReader(data), int64(len(data)))
}

func TestReadArchive(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		spec composertest.Spec
		want composer.Manifest
	}{
		{"root", composertest.Spec{Name: "acme/tools", Version: "1.0.0", License: "MIT"},
			composer.Manifest{Name: "acme/tools", Version: "1.0.0", Licenses: []string{"MIT"}}},
		{"zipball dir", composertest.Spec{Name: "acme/tools", Dir: "acme-tools-abc123", License: []string{"MIT", "Apache-2.0"}},
			composer.Manifest{Name: "acme/tools", Licenses: []string{"MIT", "Apache-2.0"}}},
		{"bom", composertest.Spec{Raw: []byte("\xef\xbb\xbf" + `{"name":"a/b"}`)}, composer.Manifest{Name: "a/b"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			m, err := read(t, composertest.Build(t, c.spec))
			if err != nil {
				t.Fatal(err)
			}

			if m.Name != c.want.Name || m.Version != c.want.Version || !reflect.DeepEqual(m.Licenses, c.want.Licenses) {
				t.Fatalf("got %+v, want %+v", m, c.want)
			}
		})
	}
}

func TestReadArchiveRejects(t *testing.T) {
	t.Parallel()

	two := func() []byte {
		var buf bytes.Buffer

		zw := zip.NewWriter(&buf)
		for _, n := range []string{"a/composer.json", "b/other.txt"} {
			w, _ := zw.Create(n)
			_, _ = w.Write([]byte(`{"name":"a/b"}`))
		}

		_ = zw.Close()

		return buf.Bytes()
	}()

	for name, data := range map[string][]byte{
		"not a zip":        []byte("hello"),
		"no manifest":      composertest.Build(t, composertest.Spec{Raw: []byte(`{}`), Files: map[string]string{"x": "y"}})[:0],
		"two directories":  two,
		"no name":          composertest.Build(t, composertest.Spec{Raw: []byte(`{"description":"x"}`)}),
		"upper case name":  composertest.Build(t, composertest.Spec{Name: "Acme/Tools"}),
		"no vendor":        composertest.Build(t, composertest.Spec{Name: "tools"}),
		"array manifest":   composertest.Build(t, composertest.Spec{Raw: []byte(`[]`)}),
		"bad license":      composertest.Build(t, composertest.Spec{Name: "a/b", License: 5}),
		"bad version type": composertest.Build(t, composertest.Spec{Raw: []byte(`{"name":"a/b","version":1}`)}),
		"huge manifest":    composertest.Build(t, composertest.Spec{Raw: []byte(`{"name":"a/b","x":"` + strings.Repeat("a", composer.MaxManifestBytes) + `"}`)}),
	} {
		if _, err := read(t, data); !errors.Is(err, composer.ErrInvalidArchive) {
			t.Errorf("%s: err = %v, want ErrInvalidArchive", name, err)
		}
	}
}

func FuzzReadArchive(f *testing.F) {
	f.Add(composertest.Build(f, composertest.Spec{Name: "acme/tools", Version: "1.0.0", License: "MIT"}))
	f.Add(composertest.Build(f, composertest.Spec{Name: "acme/tools", Dir: "x-y-1"}))
	f.Add([]byte("PK\x03\x04"))

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := composer.ReadArchive(bytes.NewReader(data), int64(len(data)))
		if err == nil && !composer.ValidName(m.Name) {
			t.Fatalf("accepted the invalid name %q", m.Name)
		}
	})
}
