// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/pkg/ansible"
)

const sum = "6dc970c9" + "6dc970c9" + "6dc970c9" + "6dc970c9" + "6dc970c9" + "6dc970c9" + "6dc970c9" + "6dc970c9"

func detail(extra string) string {
	return `{"version":"1.5.4","href":"https://up/x/","name":"posix","namespace":{"name":"ansible"},
	"collection":{"name":"posix","href":"https://up/c/"},"download_url":"https://up/dl/ansible-posix-1.5.4.tar.gz",
	"artifact":{"filename":"ansible-posix-1.5.4.tar.gz","sha256":"` + strings.ToUpper(sum) + `","size":7},
	"metadata":{"dependencies":{"community.general":">=1.0.0","a.b":""},"license":["GPL"]}` + extra + `}`
}

func TestParseVersionDocRelinks(t *testing.T) {
	t.Parallel()

	d, err := ansible.ParseVersionDoc(strings.NewReader(detail("")), "ansible", "posix", "1.5.4")
	if err != nil {
		t.Fatal(err)
	}

	d.Relink(ansible.Links{Root: "https://oa/repos/r/api/v3"})

	if d.DownloadURL != "https://oa/repos/r/api/v3/artifacts/collections/ansible-posix-1.5.4.tar.gz" ||
		d.Href != "https://oa/repos/r/api/v3/collections/ansible/posix/versions/1.5.4/" ||
		d.Collection.Href != "https://oa/repos/r/api/v3/collections/ansible/posix/" {
		t.Errorf("links not rewritten: %+v", d)
	}

	if d.Artifact.SHA256 != sum || d.Signatures == nil || d.Marks == nil {
		t.Errorf("digest %q, signatures %v, marks %v", d.Artifact.SHA256, d.Signatures, d.Marks)
	}

	if got := d.Dependencies(); got["community.general"] != ">=1.0.0" || len(got) != 2 {
		t.Errorf("dependencies = %v", got)
	}
}

func TestParseVersionDocRejects(t *testing.T) {
	t.Parallel()

	bad := map[string]string{
		"other version":  strings.Replace(detail(""), `"version":"1.5.4"`, `"version":"1.5.5"`, 1),
		"other name":     strings.Replace(detail(""), `"name":"posix","namespace"`, `"name":"other","namespace"`, 1),
		"file name":      strings.Replace(detail(""), `"filename":"ansible-posix-1.5.4.tar.gz"`, `"filename":"../x.tar.gz"`, 1),
		"digest":         strings.Replace(detail(""), strings.ToUpper(sum), "zz", 1),
		"dependency key": strings.Replace(detail(""), `"a.b":""`, `"nodots":"*"`, 1),
		"dependency req": strings.Replace(detail(""), `"a.b":""`, `"a.b":"~~"`, 1),
		"not json":       "<html>",
	}

	for name, doc := range bad {
		if _, err := ansible.ParseVersionDoc(strings.NewReader(doc), "ansible", "posix", "1.5.4"); !errors.Is(err, ansible.ErrDoc) {
			t.Errorf("%s: err = %v, want ErrDoc", name, err)
		}
	}
}

func TestParseVersionPage(t *testing.T) {
	t.Parallel()

	doc := `{"meta":{"count":3},"links":{"next":"https://up/next","previous":null},` +
		`"data":[{"version":"1.0.0","created_at":"x"},{"version":"2.0.0-rc.1","requires_ansible":">=2.15"}]}`

	p, err := ansible.ParseVersionPage(strings.NewReader(doc))
	if err != nil || p.Count != 3 || p.Next != "https://up/next" || len(p.Versions) != 2 {
		t.Fatalf("page = %+v, %v", p, err)
	}

	last, err := ansible.ParseVersionPage(strings.NewReader(`{"meta":{"count":0},"links":{"next":null},"data":[]}`))
	if err != nil || last.Next != "" {
		t.Errorf("last page = %+v, %v", last, err)
	}

	if _, err := ansible.ParseVersionPage(strings.NewReader(`{"data":[{"version":"x"}]}`)); !errors.Is(err, ansible.ErrDoc) {
		t.Errorf("invalid version: err = %v", err)
	}
}

func TestCollectionDocHighestIsRelease(t *testing.T) {
	t.Parallel()

	refs := []ansible.VersionRef{
		{Version: "3.0.0-rc.1", CreatedAt: "2024-03", UpdatedAt: "2024-04"},
		{Version: "2.0.0", CreatedAt: "2024-02", UpdatedAt: "2024-02"},
		{Version: "1.0.0", CreatedAt: "2024-01", UpdatedAt: "2024-01"},
	}
	ansible.NewestFirst(refs)

	d := ansible.NewCollectionDoc(ansible.Links{Root: "https://oa/v3"}, "a", "b", refs)
	if d.HighestVersion.Version != "2.0.0" || d.CreatedAt != "2024-01" || d.UpdatedAt != "2024-04" {
		t.Errorf("doc = %+v", d)
	}
}

func TestPageParamsAndWindow(t *testing.T) {
	t.Parallel()

	limit, offset, err := ansible.PageParams(url.Values{"limit": {"5000"}, "offset": {"2"}})
	if err != nil || limit != ansible.MaxLimit || offset != 2 {
		t.Errorf("PageParams = %d, %d, %v", limit, offset, err)
	}

	for _, q := range []url.Values{{"limit": {"0"}}, {"offset": {"-1"}}, {"limit": {"x"}}} {
		if _, _, err := ansible.PageParams(q); !errors.Is(err, ansible.ErrPagination) {
			t.Errorf("PageParams(%v) err = %v", q, err)
		}
	}

	if got := ansible.Window([]int{1, 2, 3}, 2, 2); len(got) != 1 || got[0] != 3 {
		t.Errorf("Window = %v", got)
	}

	if got := ansible.Window([]int{1}, 2, 5); got != nil {
		t.Errorf("Window past the end = %v", got)
	}
}
