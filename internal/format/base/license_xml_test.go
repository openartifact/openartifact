// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"slices"
	"testing"
)

func TestDescriptorLicenses(t *testing.T) {
	tests := []struct {
		name, file, body string
		want             []string
	}{
		{"ivy", "ivy-1.0.xml", `<ivy-module><info><license name="GPL-3.0"/><license name=" MIT "/></info></ivy-module>`, []string{"GPL-3.0", "MIT"}},
		{"pom", "a-1.0.pom", `<project xmlns="http://maven.apache.org/POM/4.0.0"><licenses><license><name>Apache-2.0</name></license></licenses></project>`, []string{"Apache-2.0"}},
		{"no license", "ivy.xml", `<ivy-module><info/></ivy-module>`, nil},
		{"malformed", "a.pom", `<project><licenses>`, nil},
		{"not a descriptor", "a.jar", `<info><license name="GPL-3.0"/></info>`, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescriptorLicenses(tc.file, []byte(tc.body)); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
