// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"path"
	"slices"
	"strings"
	"testing"
)

// FuzzDescriptorLicenses feeds arbitrary descriptors (an Ivy ivy.xml or a
// Maven POM from an upload) to the license extraction: only .xml and .pom
// files declare licenses, names are trimmed and non-empty, and there are no
// more of them than the document has license elements.
func FuzzDescriptorLicenses(f *testing.F) {
	f.Add(
		"ivy.xml",
		[]byte(
			`<ivy-module><info organisation="o" module="m"><license name=" Apache-2.0 " url="u"/><license name=""/></info></ivy-module>`,
		),
	)
	f.Add(
		"lib-1.0.pom",
		[]byte(`<project><licenses><license><name>MIT</name></license><license><name>  </name></license></licenses></project>`),
	)
	f.Add(
		"a.pom",
		[]byte(
			`<project xmlns="http://maven.apache.org/POM/4.0.0"><licenses><license><name><![CDATA[BSD &amp; Co]]></name></license></licenses></project>`,
		),
	)
	f.Add("lib.jar", []byte(`<info><license name="MIT"/></info>`))
	f.Add("ivy.xml", []byte(`<!DOCTYPE x [<!ENTITY a "&a;&a;">]><info>&a;</info>`))
	f.Add("ivy.xml", []byte(``))

	f.Fuzz(func(t *testing.T, filename string, data []byte) {
		got := DescriptorLicenses(filename, data)

		ext := strings.ToLower(path.Ext(filename))
		descriptor := ext == ".xml" || ext == ".pom"

		if !descriptor && got != nil {
			t.Fatalf("DescriptorLicenses(%q) = %q for a file that is no descriptor", filename, got)
		}

		for _, l := range got {
			if l == "" || l != strings.TrimSpace(l) {
				t.Fatalf("DescriptorLicenses(%q) returned %q among %q", filename, l, got)
			}
		}

		if len(got)*len("<license") > len(data) {
			t.Fatalf("DescriptorLicenses returned %d licenses from %d bytes", len(got), len(data))
		}

		if other := DescriptorLicenses("ivy.xml", data); descriptor && !slices.Equal(got, other) {
			t.Fatalf("DescriptorLicenses(%q) = %q but as ivy.xml %q", filename, got, other)
		}
	})
}
