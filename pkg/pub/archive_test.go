// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/pub/pubtest"
)

func TestReadArchive(t *testing.T) {
	t.Parallel()

	data := pubtest.Build(t, pubtest.Spec{Name: "demo_pkg", Version: "1.2.3-rc.1+4"})

	p, err := ReadArchive(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if p.Name != "demo_pkg" || p.Version != "1.2.3-rc.1+4" {
		t.Fatalf("identity = %s %s", p.Name, p.Version)
	}

	var spec map[string]any
	if err := json.Unmarshal(p.Pubspec, &spec); err != nil {
		t.Fatal(err)
	}

	if spec["description"] != "A demo package." || spec["dependencies"].(map[string]any)["collection"] != "^1.17.0" {
		t.Errorf("pubspec = %s", p.Pubspec)
	}
}

func TestReadArchiveRejects(t *testing.T) {
	t.Parallel()

	spec := func(s string) []pubtest.Entry { return []pubtest.Entry{{Name: "pubspec.yaml", Body: []byte(s)}} }

	deep := strings.Repeat("[", 100) + strings.Repeat("]", 100)

	for name, entries := range map[string][]pubtest.Entry{
		"no pubspec":      {{Name: "lib/a.dart", Body: []byte("x")}},
		"nested pubspec":  {{Name: "pkg/pubspec.yaml", Body: []byte("name: a\nversion: 1.0.0\n")}},
		"duplicate":       append(spec("name: a\nversion: 1.0.0\n"), spec("name: a\nversion: 1.0.0\n")...),
		"bad name":        spec("name: Bad-Name\nversion: 1.0.0\n"),
		"reserved name":   spec("name: class\nversion: 1.0.0\n"),
		"digit name":      spec("name: 1abc\nversion: 1.0.0\n"),
		"no version":      spec("name: a\n"),
		"bad version":     spec("name: a\nversion: v1\n"),
		"float version":   spec("name: a\nversion: 1.0\n"),
		"not a mapping":   spec("- a\n- b\n"),
		"bad yaml":        spec("name: [a\n"),
		"deep nesting":    spec("name: a\nversion: 1.0.0\nx: " + deep + "\n"),
		"complex key":     spec("name: a\nversion: 1.0.0\n? [a]\n: b\n"),
		"traversal":       append(spec("name: a\nversion: 1.0.0\n"), pubtest.Entry{Name: "../evil", Body: []byte("x")}),
		"absolute":        append(spec("name: a\nversion: 1.0.0\n"), pubtest.Entry{Name: "/etc/passwd", Body: []byte("x")}),
		"symlink":         append(spec("name: a\nversion: 1.0.0\n"), pubtest.Entry{Name: "link", Type: tar.TypeSymlink}),
		"huge pubspec":    spec("name: a\nversion: 1.0.0\nx: " + strings.Repeat("y", MaxPubspecBytes) + "\n"),
		"nan":             spec("name: a\nversion: 1.0.0\nx: .nan\n"),
		"alias expansion": spec(aliasBomb()),
	} {
		_, err := ReadArchive(bytes.NewReader(pubtest.Tar(t, entries)))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}

	for name, data := range map[string][]byte{"empty": nil, "not gzip": []byte("plain text"), "truncated": {0x1f, 0x8b, 0x08}} {
		if _, err := ReadArchive(bytes.NewReader(data)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// aliasBomb is a pubspec whose aliases expand to far more than its size.
func aliasBomb() string {
	var b strings.Builder

	b.WriteString("name: a\nversion: 1.0.0\nk0: &a0 [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]\n")

	for i := 1; i < 12; i++ {
		fmt.Fprintf(&b, "k%[1]d: &a%[1]d [", i)

		for range 9 {
			fmt.Fprintf(&b, "*a%d, ", i-1)
		}

		fmt.Fprintf(&b, "*a%d]\n", i-1)
	}

	return b.String()
}

func TestReadArchiveToleratesGlobalPaxHeader(t *testing.T) {
	t.Parallel()

	entries := []pubtest.Entry{
		{Name: "pax_global_header", Type: tar.TypeXGlobalHeader},
		{Name: "./pubspec.yaml", Body: []byte("name: a\nversion: 1.0.0\n")},
		{Name: "lib/", Type: tar.TypeDir},
	}

	if _, err := ReadArchive(bytes.NewReader(pubtest.Tar(t, entries))); err != nil {
		t.Fatal(err)
	}
}

func TestParsePubspecKeepsYAMLTypes(t *testing.T) {
	t.Parallel()

	p, err := ParsePubspec([]byte("name: a\nversion: 1.0.0\nflag: true\nn: 3\nwhen: 2024-01-02\nlist: [1, two]\n1: numeric key\n"))
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(p.Pubspec, &got); err != nil {
		t.Fatal(err)
	}

	if got["flag"] != true || got["n"] != float64(3) || got["when"] != "2024-01-02T00:00:00Z" || got["1"] != "numeric key" {
		t.Errorf("pubspec = %s", p.Pubspec)
	}
}
