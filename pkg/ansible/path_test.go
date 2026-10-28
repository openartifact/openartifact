// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible_test

import (
	"testing"

	"github.com/openartifact/openartifact/pkg/ansible"
)

func TestParsePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rel  string
		want ansible.Path
		ok   bool
	}{
		{"api/", ansible.Path{Kind: ansible.KindRoot}, true},
		{"/api", ansible.Path{Kind: ansible.KindRoot}, true},
		{"api/v3/", ansible.Path{Kind: ansible.KindV3}, true},
		{"api/v3/collections/", ansible.Path{Kind: ansible.KindIndex}, true},
		{"api/v3/collections/acme/tools/", ansible.Path{Kind: ansible.KindCollection, Namespace: "acme", Name: "tools"}, true},
		{"api/v3/collections/acme/tools/versions/", ansible.Path{Kind: ansible.KindVersions, Namespace: "acme", Name: "tools"}, true},
		{
			"api/v3/collections/acme/tools/versions/1.0.0-rc.1",
			ansible.Path{Kind: ansible.KindVersion, Namespace: "acme", Name: "tools", Version: "1.0.0-rc.1"}, true,
		},
		{
			"api/v3/artifacts/collections/acme-tools-1.2.3.tar.gz",
			ansible.Path{Kind: ansible.KindDownload, Namespace: "acme", Name: "tools", Version: "1.2.3"}, true,
		},
		{"api/v3/collections/Acme/tools/", ansible.Path{}, false},
		{"api/v3/collections/acme/tools/versions/not-semver/", ansible.Path{}, false},
		{"api/v3/collections/acme/tools/other/", ansible.Path{}, false},
		{"api/v3/collections/acme/tools/versions/1.0.0/extra", ansible.Path{}, false},
		{"api/v3/artifacts/collections/../x.tar.gz", ansible.Path{}, false},
		{"api/v3/artifacts/collections/acme-tools-1.2.3.zip", ansible.Path{}, false},
		{"api/v3/collections/acme/%2e%2e/", ansible.Path{}, false},
		{"api/v2/collections/", ansible.Path{}, false},
		{"api/v3/imports/collections/1/", ansible.Path{}, false},
		{"", ansible.Path{}, false},
	}

	for _, tt := range tests {
		got, ok := ansible.ParsePath(tt.rel)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("ParsePath(%q) = %+v, %v; want %+v, %v", tt.rel, got, ok, tt.want, tt.ok)
		}
	}
}
