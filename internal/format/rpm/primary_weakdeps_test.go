// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"strings"
	"testing"

	pkgrpm "github.com/openartifact/openartifact/pkg/rpm"
)

func TestGeneratePrimaryXML_WeakDepsAndOrder(t *testing.T) {
	pkg := pkgrpm.Package{
		Name: "p", Version: "1", Release: "1", Arch: "noarch",
		Provides:    []pkgrpm.Dependency{{Name: "p", Flags: pkgrpm.SENSE_EQUAL, Version: "1-1", Epoch: 1}},
		Requires:    []pkgrpm.Dependency{{Name: "rpmlib(X)", Flags: pkgrpm.SENSE_RPMLIB | pkgrpm.SENSE_LESSEQUAL, Version: "1"}, {Name: "sh", Flags: pkgrpm.SENSE_PREREQ}, {Name: "lib"}},
		Conflicts:   []pkgrpm.Dependency{{Name: "c", Flags: pkgrpm.SENSE_LESS, Version: "2.0"}},
		Obsoletes:   []pkgrpm.Dependency{{Name: "o"}},
		Recommends:  []pkgrpm.Dependency{{Name: "rec"}},
		Suggests:    []pkgrpm.Dependency{{Name: "sug", Flags: pkgrpm.SENSE_GREATEREQUAL, Version: "1.0"}},
		Supplements: []pkgrpm.Dependency{{Name: "sup"}},
		Enhances:    []pkgrpm.Dependency{{Name: "enh"}},
	}

	out, err := GeneratePrimaryXML([]pkgrpm.Package{pkg}, "")
	if err != nil {
		t.Fatal(err)
	}

	xml := string(out)

	// createrepo_c order.
	last := -1

	for _, tag := range []string{"<rpm:provides>", "<rpm:requires>", "<rpm:conflicts>", "<rpm:obsoletes>", "<rpm:suggests>", "<rpm:enhances>", "<rpm:recommends>", "<rpm:supplements>"} {
		i := strings.Index(xml, tag)
		if i < 0 {
			t.Fatalf("%s missing in:\n%s", tag, xml)
		}

		if i < last {
			t.Errorf("%s out of createrepo_c order", tag)
		}

		last = i
	}

	for _, want := range []string{
		`<rpm:entry name="rec"></rpm:entry>`,
		`<rpm:entry name="sug" flags="GE" epoch="0" ver="1.0"></rpm:entry>`,
		`<rpm:entry name="p" flags="EQ" epoch="1" ver="1" rel="1"></rpm:entry>`,
		`<rpm:entry name="c" flags="LT" epoch="0" ver="2.0"></rpm:entry>`,
		`<rpm:entry name="sh" pre="1"></rpm:entry>`,
		`<rpm:entry name="lib"></rpm:entry>`,
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("missing %s in:\n%s", want, xml)
		}
	}

	if strings.Contains(xml, "rpmlib(") {
		t.Error("rpmlib requirement must be omitted")
	}
}

func TestFilterRequiresMatchesCreaterepoC(t *testing.T) {
	const autoFlag = 1 << 26 // rpm's auto-generated marker bit; must not count as versioned

	pkg := &pkgrpm.Package{
		Provides: []pkgrpm.Dependency{
			{Name: "bash", Flags: pkgrpm.SENSE_EQUAL, Version: "5-1"},
			{Name: "config(bash)", Flags: pkgrpm.SENSE_EQUAL, Version: "5-1"},
			{Name: "/bin/sh"},
		},
		Files: []pkgrpm.FileEntry{{Path: "/usr/bin/sh"}},
		Requires: []pkgrpm.Dependency{
			{Name: "/usr/bin/sh"}, // packaged file: dropped
			{Name: "config(bash)", Flags: pkgrpm.SENSE_EQUAL, Version: "5-1"}, // self-provided: dropped
			{Name: "/bin/sh", Flags: pkgrpm.SENSE_PREREQ},                     // pre: kept
			{Name: "/bin/sh", Flags: pkgrpm.SENSE_PREREQ},                     // duplicate: dropped
			{Name: "libc.so.6()(64bit)", Flags: autoFlag},
			{Name: "libc.so.6(GLIBC_2.9)(64bit)", Flags: autoFlag},
			{Name: "libc.so.6(GLIBC_2.34)(64bit)", Flags: autoFlag},
			{Name: "libc.so.6(GLIBC_2.4)(64bit)", Flags: autoFlag},
			{Name: "libtinfo.so.6()(64bit)", Flags: autoFlag},
		},
	}

	var got []string
	for _, d := range filterRequires(pkg) {
		got = append(got, d.Name)
	}

	want := []string{"/bin/sh", "libtinfo.so.6()(64bit)", "libc.so.6(GLIBC_2.34)(64bit)"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("filterRequires = %v, want %v", got, want)
	}
}

// filterRequires runs a fresh requiresFilter over pkg.
func filterRequires(pkg *pkgrpm.Package) []pkgrpm.Dependency {
	return newRequiresFilter().filter(pkg)
}
