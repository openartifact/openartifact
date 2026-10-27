// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ocipath

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		rest string
		want Request
	}{
		{"manifest single segment", "reg/manifests/latest", Request{KindManifest, "reg", "latest"}},
		{"manifest multi segment", "reg/org/team/app/manifests/sha256:abcd", Request{KindManifest, "reg/org/team/app", "sha256:abcd"}},
		{"blob", "reg/org/app/blobs/sha256:abcd", Request{KindBlob, "reg/org/app", "sha256:abcd"}},
		{"upload start", "reg/org/app/blobs/uploads/", Request{Kind: KindUploadStart, Name: "reg/org/app"}},
		{"upload start without slash", "reg/org/app/blobs/uploads", Request{Kind: KindUploadStart, Name: "reg/org/app"}},
		{"upload session", "reg/org/app/blobs/uploads/8f1c", Request{KindUpload, "reg/org/app", "8f1c"}},
		{"tags list", "reg/org/app/tags/list", Request{Kind: KindTags, Name: "reg/org/app"}},
		{"referrers", "reg/org/app/referrers/sha256:abcd", Request{KindReferrers, "reg/org/app", "sha256:abcd"}},
		{
			"name containing a marker segment",
			"reg/manifests/latest/manifests/sha256:abcd",
			Request{KindManifest, "reg/manifests/latest", "sha256:abcd"},
		},
		{"name ending in blobs", "reg/blobs/manifests/1", Request{KindManifest, "reg/blobs", "1"}},
		{"name containing blobs/uploads", "reg/blobs/uploads/manifests/1", Request{KindManifest, "reg/blobs/uploads", "1"}},
		{"no sub-resource", "reg/org/app", Request{Name: "reg/org/app"}},
		{"marker without name", "manifests/latest", Request{Name: "manifests/latest"}},
		{"empty reference", "reg/manifests/", Request{Name: "reg/manifests/"}},
		{"empty", "", Request{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.rest); got != tc.want {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.rest, got, tc.want)
			}
		})
	}
}

func TestSplitNameAndResource(t *testing.T) {
	tests := []struct {
		rest                string
		wantRepo, wantImage string
		wantResource        string
	}{
		{"reg/manifests/1", "reg", "", "manifests/1"},
		{"reg/org/team/app/manifests/1", "reg", "org/team/app", "org/team/app/manifests/1"},
		{"reg/org/app/blobs/uploads/", "reg", "org/app", "org/app/blobs/uploads/"},
		{"reg/org/app/tags/list", "reg", "org/app", "org/app/tags/list"},
		{"reg", "reg", "", ""},
		{"reg/org/app", "reg", "org/app", "org/app"},
	}

	for _, tc := range tests {
		t.Run(tc.rest, func(t *testing.T) {
			req := Parse(tc.rest)

			repo, image := SplitName(req.Name)
			if repo != tc.wantRepo || image != tc.wantImage {
				t.Fatalf("SplitName(%q) = (%q, %q), want (%q, %q)", req.Name, repo, image, tc.wantRepo, tc.wantImage)
			}

			if got := Resource(tc.rest, req.Name); got != tc.wantResource {
				t.Fatalf("Resource(%q, %q) = %q, want %q", tc.rest, req.Name, got, tc.wantResource)
			}
		})
	}
}

func TestValidImage(t *testing.T) {
	valid := []string{"", "app", "org/app", "org/team/app", "a.b/c_d/e__f/g-h", "a--b", "0/1", "app/manifests"}
	invalid := []string{"App", "org//app", "/org", "org/", "-app", "app-", "a..b", "a___b", "a/B", "org/app:1", "a b", "é"}

	for _, v := range valid {
		if !ValidImage(v) {
			t.Errorf("ValidImage(%q) = false, want true", v)
		}
	}

	for _, v := range invalid {
		if ValidImage(v) {
			t.Errorf("ValidImage(%q) = true, want false", v)
		}
	}
}
