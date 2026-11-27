// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import "testing"

func TestParsePath(t *testing.T) {
	t.Parallel()

	for rel, want := range map[string]Path{
		"api/packages/http":                     {Kind: KindPackage, Name: "http"},
		"api/packages/versions":                 {Kind: KindPackage, Name: "versions"},
		"api/packages/http/versions/1.0.0":      {Kind: KindVersion, Name: "http", Version: "1.0.0"},
		"api/packages/http/versions/1.0.0-b+1":  {Kind: KindVersion, Name: "http", Version: "1.0.0-b+1"},
		"api/archives/http-1.0.0.tar.gz":        {Kind: KindArchive, Name: "http", Version: "1.0.0"},
		"api/packages/versions/new":             {Kind: KindNew},
		"api/packages/versions/newUpload":       {Kind: KindUpload},
		"api/packages/versions/newUploadFinish": {Kind: KindFinish},
	} {
		got, ok := ParsePath(rel)
		if !ok || got != want {
			t.Errorf("ParsePath(%q) = %+v %v, want %+v", rel, got, ok, want)
		}
	}

	for _, rel := range []string{
		"", "api", "api/packages", "api/packages/", "api/packages/Http", "api/packages/http/versions/x",
		"api/packages/http/versions", "api/packages/http/other/1.0.0", "api/archives/http.tar.gz", "api/archives/x/y-1.0.0.tar.gz",
		"api/packages/../archives/x", "packages/http", "api/packages/http/versions/1.0.0/extra",
	} {
		if got, ok := ParsePath(rel); ok {
			t.Errorf("ParsePath(%q) = %+v, want no match", rel, got)
		}
	}

	if PackagePath("a") != "api/packages/a" || ArchivePath("a", "1.0.0") != "api/archives/a-1.0.0.tar.gz" {
		t.Error("path builders disagree with the grammar")
	}
}
