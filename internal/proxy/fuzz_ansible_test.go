// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"net/url"
	"testing"

	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

// FuzzAnsibleVersionDoc feeds arbitrary upstream bodies to the version document
// verifier of the Ansible proxy: none may panic, and an accepted document
// always names a download that resolves to an absolute http(s) URL.
func FuzzAnsibleVersionDoc(f *testing.F) {
	f.Add([]byte(`{"version":"1.0.0","name":"demo","namespace":{"name":"ns"},"download_url":"https://h/a.tar.gz",` +
		`"artifact":{"filename":"ns-demo-1.0.0.tar.gz","sha256":"` + string(bytes.Repeat([]byte("a"), 64)) + `","size":1}}`))
	f.Add([]byte(`{"version":"1.0.0"}`))
	f.Add([]byte(`[`))

	p := pkgansible.Path{Kind: pkgansible.KindVersion, Namespace: "ns", Name: "demo", Version: "1.0.0"}

	f.Fuzz(func(t *testing.T, data []byte) {
		if err := ansibleVerifyDoc(p)(bytes.NewReader(data)); err != nil {
			return
		}

		doc, err := pkgansible.ParseVersionDoc(bytes.NewReader(data), "ns", "demo", "1.0.0")
		if err != nil {
			t.Fatalf("verifier accepted a document ParseVersionDoc refuses: %v", err)
		}

		if target, err := ansibleDownloadTarget("https://galaxy.example/x/", doc.DownloadURL); err == nil {
			if u, perr := url.Parse(target); perr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				t.Fatalf("download target %q is not an absolute http(s) URL", target)
			}
		}
	})
}

// FuzzAnsibleVersionList feeds arbitrary version pages to the list normalizer:
// none may panic, and what it accepts is a non-empty, duplicate-free list that
// reads back as a version page.
func FuzzAnsibleVersionList(f *testing.F) {
	f.Add([]byte(`{"meta":{"count":2},"links":{"next":null},"data":[{"version":"1.0.0"},{"version":"1.0.0"},{"version":"2.0.0"}]}`))
	f.Add([]byte(`{"data":[]}`))
	f.Add([]byte(`{"data":[{"version":"x"}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		page, err := pkgansible.ParseVersionPage(bytes.NewReader(data))
		if err != nil {
			return
		}

		body, err := ansibleVersionListBody(page.Versions)
		if err != nil {
			return
		}

		back, err := pkgansible.ParseVersionPage(bytes.NewReader(body))
		if err != nil || len(back.Versions) == 0 || back.Count != len(back.Versions) {
			t.Fatalf("normalized list does not read back: %v %+v", err, back)
		}

		seen := map[string]bool{}

		for _, v := range back.Versions {
			if seen[v.Version] {
				t.Fatalf("duplicate version %q", v.Version)
			}

			seen[v.Version] = true
		}
	})
}
