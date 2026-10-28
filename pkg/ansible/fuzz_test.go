// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"bytes"
	"testing"

	"github.com/openartifact/openartifact/pkg/ansible/ansibletest"
)

// FuzzRead feeds Read arbitrary bytes: it must never panic, and an artifact it
// accepts must carry valid identity fields.
func FuzzRead(f *testing.F) {
	f.Add(ansibletest.Build(f, ansibletest.Spec{
		Namespace: "acme", Name: "tools", Version: "1.2.3-rc.1+b5", License: "MIT",
		Dependencies: map[string]string{"ansible.posix": ">=1.0.0,<2.0.0"}, RequiresAnsible: ">=2.15.0",
		Files: map[string]string{"plugins/modules/hello.py": "print('hi')\n"},
	}))
	f.Add(ansibletest.Build(f, ansibletest.Spec{Namespace: "a", Name: "b", Version: "0.0.1"}))
	f.Add([]byte{})
	f.Add([]byte("\x1f\x8b\x08\x00"))

	f.Fuzz(func(t *testing.T, data []byte) {
		col, err := Read(bytes.NewReader(data))
		if err != nil {
			return
		}

		if !ValidName(col.Namespace) || !ValidName(col.Name) || !ValidVersion(col.Version) {
			t.Fatalf("accepted an invalid identity: %s.%s %s", col.Namespace, col.Name, col.Version)
		}
	})
}

// FuzzParseFileName checks that a parsed file name round-trips.
func FuzzParseFileName(f *testing.F) {
	f.Add("acme-tools-1.0.0.tar.gz")
	f.Add("a-b-1.0.0-rc.1+x.tar.gz")
	f.Add("--.tar.gz")

	f.Fuzz(func(t *testing.T, file string) {
		ns, name, version, ok := ParseFileName(file)
		if ok && FileName(ns, name, version) != file {
			t.Fatalf("%q parsed to %q %q %q", file, ns, name, version)
		}
	})
}

// FuzzValidRequirement checks the requirement validator never panics.
func FuzzValidRequirement(f *testing.F) {
	f.Add(">=1.0.0,<2.0.0")
	f.Add("*")

	f.Fuzz(func(_ *testing.T, spec string) { _ = ValidRequirement(spec) })
}
