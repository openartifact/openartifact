// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/ansible"
	"github.com/openartifact/openartifact/pkg/ansible/ansibletest"
)

var demo = ansibletest.Spec{ //nolint:gochecknoglobals // read-only test fixture
	Namespace: "acme", Name: "tools", Version: "1.2.3-rc.1+b5", License: "MIT",
	Dependencies:    map[string]string{"ansible.posix": ">=1.0.0,<2.0.0", "community.general": "*"},
	RequiresAnsible: ">=2.15.0",
	Files:           map[string]string{"plugins/modules/hello.py": "print('hi')\n"},
}

func TestReadValid(t *testing.T) {
	col, err := ansible.Read(bytes.NewReader(ansibletest.Build(t, demo)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if col.Namespace != "acme" || col.Name != "tools" || col.Version != "1.2.3-rc.1+b5" {
		t.Errorf("identity = %s.%s %s", col.Namespace, col.Name, col.Version)
	}

	if col.Dependencies["ansible.posix"] != ">=1.0.0,<2.0.0" || len(col.Dependencies) != 2 {
		t.Errorf("dependencies = %v", col.Dependencies)
	}

	if len(col.Licenses) != 1 || col.Licenses[0] != "MIT" {
		t.Errorf("licenses = %v", col.Licenses)
	}

	if col.RequiresAnsible != ">=2.15.0" {
		t.Errorf("requires_ansible = %q", col.RequiresAnsible)
	}

	var info map[string]any
	if err := json.Unmarshal(col.Info, &info); err != nil || info["description"] != "A demo collection." {
		t.Errorf("info = %s (%v)", col.Info, err)
	}
}

func TestReadTolerates(t *testing.T) {
	spec := demo
	spec.RequiresAnsible = ""
	entries := ansibletest.Entries(t, spec)
	// A "./" prefix and directory entries are ordinary in tar files.
	for i := range entries {
		entries[i].Name = "./" + entries[i].Name
	}

	entries = append(entries, ansibletest.Entry{Name: "./plugins/", Type: tar.TypeDir})

	col, err := ansible.Read(bytes.NewReader(ansibletest.Tar(t, entries)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if col.RequiresAnsible != "" {
		t.Errorf("requires_ansible = %q, want none", col.RequiresAnsible)
	}
}

// mutate returns the artifact of demo with change applied to its entries.
func mutate(t *testing.T, change func([]ansibletest.Entry) []ansibletest.Entry) []byte {
	t.Helper()

	return ansibletest.Tar(t, change(ansibletest.Entries(t, demo)))
}

func replaceBody(entries []ansibletest.Entry, name string, edit func(string) string) []ansibletest.Entry {
	for i := range entries {
		if entries[i].Name == name {
			entries[i].Body = []byte(edit(string(entries[i].Body)))
		}
	}

	return entries
}

func TestReadRejects(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		reason string
	}{
		{"not gzip", []byte("plain text"), "gzip"},
		{"no manifest", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry { return e[1:] }), "MANIFEST.json is missing"},
		{
			"no files json",
			mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry { return append(e[:1], e[2:]...) }),
			"FILES.json is missing",
		},
		{"bad namespace", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return replaceBody(
				e,
				"MANIFEST.json",
				func(s string) string { return strings.Replace(s, `"namespace":"acme"`, `"namespace":"Acme-X"`, 1) },
			)
		}), "namespace"},
		{"bad version", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return replaceBody(e, "MANIFEST.json", func(s string) string { return strings.Replace(s, `"1.2.3-rc.1+b5"`, `"1.2"`, 1) })
		}), "semantic version"},
		{"bad dependency name", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return replaceBody(e, "MANIFEST.json", func(s string) string { return strings.Replace(s, "ansible.posix", "posix", 1) })
		}), "namespace.name"},
		{"bad dependency spec", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return replaceBody(e, "MANIFEST.json", func(s string) string { return strings.Replace(s, `"*"`, `"@@"`, 1) })
		}), "requirement"},
		{"tampered file", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return replaceBody(e, "README.md", func(string) string { return "changed" })
		}), "checksum"},
		{"tampered files json", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return replaceBody(e, "FILES.json", func(s string) string { return s + " " })
		}), "does not match the checksum"},
		{"unlisted file", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return append(e, ansibletest.Entry{Name: "extra.sh", Body: []byte("x")})
		}), "not listed"},
		{
			"missing listed file",
			mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry { return e[:len(e)-1] }),
			"missing from the archive",
		},
		{"traversal", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return append(e, ansibletest.Entry{Name: "../evil", Body: []byte("x")})
		}), "unsafe"},
		{"absolute path", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return append(e, ansibletest.Entry{Name: "/etc/passwd", Body: []byte("x")})
		}), "unsafe"},
		{"symlink", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry {
			return append(e, ansibletest.Entry{Name: "link", Type: tar.TypeSymlink})
		}), "not a regular file"},
		{"duplicate entry", mutate(t, func(e []ansibletest.Entry) []ansibletest.Entry { return append(e, e[len(e)-1]) }), "twice"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ansible.Read(bytes.NewReader(tc.data))
			if !errors.Is(err, ansible.ErrInvalid) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("Read error = %v, want ErrInvalid mentioning %q", err, tc.reason)
			}
		})
	}
}

func TestRuntimeWithoutRequirement(t *testing.T) {
	spec := demo
	spec.RequiresAnsible = ""
	spec.Files = map[string]string{"meta/runtime.yml": "plugin_routing: {}\n"}

	col, err := ansible.Read(bytes.NewReader(ansibletest.Build(t, spec)))
	if err != nil || col.RequiresAnsible != "" {
		t.Fatalf("Read = %+v, %v", col, err)
	}
}
