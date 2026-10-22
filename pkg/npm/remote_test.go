// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		in      string
		want    Path
		invalid bool
	}{
		{in: "lodash", want: Path{Kind: KindPackument, Package: "lodash"}},
		{in: "/lodash", want: Path{Kind: KindPackument, Package: "lodash"}},
		{in: "JSONStream", want: Path{Kind: KindPackument, Package: "JSONStream"}},
		{in: "@babel%2fcore", want: Path{Kind: KindPackument, Package: "@babel/core"}},
		{in: "@babel%2Fcore", want: Path{Kind: KindPackument, Package: "@babel/core"}},
		{in: "@babel/core", want: Path{Kind: KindPackument, Package: "@babel/core"}},
		{in: "lodash/4.17.21", want: Path{Kind: KindVersion, Package: "lodash", Version: "4.17.21"}},
		{in: "@babel/core/latest", want: Path{Kind: KindVersion, Package: "@babel/core", Version: "latest"}},
		{in: "lodash/-/lodash-4.17.21.tgz", want: Path{Kind: KindTarball, Package: "lodash", File: "lodash-4.17.21.tgz"}},
		{in: "@babel/core/-/core-7.0.0.tgz", want: Path{Kind: KindTarball, Package: "@babel/core", File: "core-7.0.0.tgz"}},
		{in: "@babel%2fcore/-/core-7.0.0.tgz", want: Path{Kind: KindTarball, Package: "@babel/core", File: "core-7.0.0.tgz"}},
		{in: "", invalid: true},
		{in: "@babel", invalid: true},
		{in: "-/v1/search", invalid: true},
		{in: "-", invalid: true},
		{in: "lodash/-", invalid: true},
		{in: "lodash/-/", invalid: true},
		{in: "lodash/-/a/b", invalid: true},
		{in: "lodash/a/b", invalid: true},
		{in: "../x", invalid: true},
		{in: "lodash/-/..", invalid: true},
		{in: "lo dash", invalid: true},
		{in: "lo%64ash", invalid: true},
		{in: ".hidden", invalid: true},
		{in: "@/core", invalid: true},
	}

	for _, tt := range tests {
		got, err := ParsePath(tt.in)
		if tt.invalid {
			if err == nil {
				t.Errorf("ParsePath(%q) = %+v, want an error", tt.in, got)
			}

			continue
		}

		if err != nil || got != tt.want {
			t.Errorf("ParsePath(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
}

func TestRewriteTarballsKeepsEverythingElse(t *testing.T) {
	doc := `{"name":"@s/n","dist-tags":{"latest":"1.0.0"},"custom":{"n":12345678901234567890},
	  "versions":{
	    "1.0.0":{"name":"@s/n","version":"1.0.0","scripts":{"x":"a<b"},"dist":{"tarball":"https://r.example/@s/n/-/n-1.0.0.tgz","integrity":"sha512-AAAA","shasum":"ab","fileCount":3}},
	    "1.1.0":{"version":"1.1.0","dist":{"tarball":"https://r.example/odd/path/thing.tgz?x=1"}},
	    "1.2.0":{"version":"1.2.0"}}}`

	out, err := RewriteTarballs([]byte(doc), "@s/n", "http://oa/repos/r/")
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Custom   json.RawMessage `json:"custom"`
		Versions map[string]struct {
			Scripts map[string]string          `json:"scripts"`
			Dist    map[string]json.RawMessage `json:"dist"`
		} `json:"versions"`
	}

	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	if string(got.Custom) != `{"n":12345678901234567890}` {
		t.Errorf("unrelated field changed: %s", got.Custom)
	}

	v := got.Versions["1.0.0"]
	if string(v.Dist["tarball"]) != `"http://oa/repos/r/@s/n/-/n-1.0.0.tgz"` || string(v.Dist["integrity"]) != `"sha512-AAAA"` || string(v.Dist["fileCount"]) != "3" {
		t.Errorf("1.0.0 dist = %v", v.Dist)
	}

	if v.Scripts["x"] != "a<b" {
		t.Errorf("script mangled: %q", v.Scripts["x"])
	}

	if string(got.Versions["1.1.0"].Dist["tarball"]) != `"http://oa/repos/r/@s/n/-/thing.tgz"` {
		t.Errorf("1.1.0 tarball = %s", got.Versions["1.1.0"].Dist["tarball"])
	}

	if got.Versions["1.2.0"].Dist != nil {
		t.Errorf("a version without dist gained one: %v", got.Versions["1.2.0"].Dist)
	}
}

func TestRewriteTarballsRejectsGarbage(t *testing.T) {
	for _, doc := range []string{"", "<html>", "[]", "null", `{"versions":[1]}`} {
		if _, err := RewriteTarballs([]byte(doc), "p", "http://oa"); err == nil {
			t.Errorf("RewriteTarballs(%q) succeeded", doc)
		}
	}
}

func TestFindTarball(t *testing.T) {
	doc := `{"versions":{
	  "1.0.0":{"dist":{"tarball":"https://r/p/-/p-1.0.0.tgz","shasum":"s1"}},
	  "2.0.0":{"dist":{"tarball":"https://r/p/-/renamed.tgz","integrity":"sha512-X","shasum":"s2"}}}}`

	d, ok, err := FindTarball([]byte(doc), "p", "p-1.0.0.tgz")
	if err != nil || !ok || d.Shasum != "s1" || d.Tarball != "https://r/p/-/p-1.0.0.tgz" {
		t.Errorf("conventional name: %+v %v %v", d, ok, err)
	}

	d, ok, err = FindTarball([]byte(doc), "p", "renamed.tgz")
	if err != nil || !ok || d.Integrity != "sha512-X" {
		t.Errorf("unconventional name (scan): %+v %v %v", d, ok, err)
	}

	if _, ok, _ := FindTarball([]byte(doc), "p", "p-3.0.0.tgz"); ok {
		t.Error("unlisted tarball found")
	}
}

func TestMergePackumentsPrecedence(t *testing.T) {
	a := `{"name":"p","description":"A","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"v":"a1"}},"time":{"modified":"ma","1.0.0":"ta"}}`
	b := `{"name":"p","description":"B","readme":"rb","dist-tags":{"latest":"2.0.0","beta":"2.0.0-beta.1"},"versions":{"1.0.0":{"v":"b1"},"2.0.0":{"v":"b2"}},"time":{"modified":"mb","2.0.0":"tb"}}`
	c := `{"name":"p","dist-tags":{"canary":"3.0.0"},"versions":{"3.0.0":{"v":"c3"}}}`

	out, err := MergePackuments([][]byte{[]byte(a), []byte(b), []byte(c)})
	if err != nil {
		t.Fatal(err)
	}

	var m struct {
		Description string                       `json:"description"`
		Readme      string                       `json:"readme"`
		DistTags    map[string]string            `json:"dist-tags"` //nolint:tagliatelle // npm registry wire format
		Versions    map[string]map[string]string `json:"versions"`
		Time        map[string]string            `json:"time"`
	}

	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}

	if m.Versions["1.0.0"]["v"] != "a1" || m.Versions["2.0.0"]["v"] != "b2" || m.Versions["3.0.0"]["v"] != "c3" || len(m.Versions) != 3 {
		t.Errorf("versions = %v", m.Versions)
	}

	if m.DistTags["latest"] != "1.0.0" || m.DistTags["beta"] != "2.0.0-beta.1" || m.DistTags["canary"] != "3.0.0" {
		t.Errorf("dist-tags = %v", m.DistTags)
	}

	if m.Description != "A" || m.Readme != "rb" {
		t.Errorf("description %q readme %q: want first member's, falling back to later members for absent fields", m.Description, m.Readme)
	}

	if m.Time["modified"] != "ma" || m.Time["1.0.0"] != "ta" || m.Time["2.0.0"] != "tb" {
		t.Errorf("time = %v", m.Time)
	}
}

func TestMergePackumentsSingleAndBroken(t *testing.T) {
	one := []byte(`{"name":"p"}`)

	if out, err := MergePackuments([][]byte{one}); err != nil || string(out) != string(one) {
		t.Errorf("single document = %s, %v; want it unchanged", out, err)
	}

	out, err := MergePackuments([][]byte{[]byte("<html>"), one})
	if err != nil || !strings.Contains(string(out), `"name":"p"`) {
		t.Errorf("a broken member should be skipped: %s, %v", out, err)
	}

	if _, err := MergePackuments([][]byte{[]byte("<html>"), []byte("[]")}); err == nil {
		t.Error("nothing to merge should be an error")
	}
}

func TestVersionManifest(t *testing.T) {
	doc := []byte(`{"dist-tags":{"latest":"1.0.0","bad":5},"versions":{"1.0.0":{"v":1}}}`)

	for in, want := range map[string]bool{"1.0.0": true, "latest": true, "bad": false, "9.9.9": false} {
		raw, ok, err := VersionManifest(doc, in)
		if err != nil || ok != want {
			t.Errorf("VersionManifest(%q) = %s, %v, %v; want found=%v", in, raw, ok, err, want)
		}
	}
}

func digests(b []byte) (string, string, string) {
	s1 := sha1.Sum(b)
	s256 := sha256.Sum256(b)
	s512 := sha512.Sum512(b)

	return hex.EncodeToString(s1[:]), "sha256-" + base64.StdEncoding.EncodeToString(s256[:]), "sha512-" + base64.StdEncoding.EncodeToString(s512[:])
}

func TestVerifyDist(t *testing.T) {
	body := []byte("tarball")
	shasum, sri256, sri512 := digests(body)
	_, _, otherSRI := digests([]byte("other"))

	tests := []struct {
		name         string
		dist         Dist
		wantVerified bool
		wantErr      error
	}{
		{name: "sha512 integrity", dist: Dist{Integrity: sri512}, wantVerified: true},
		{name: "integrity wins over a wrong shasum", dist: Dist{Integrity: sri512, Shasum: "00"}, wantVerified: true},
		{name: "any digest of the strongest algorithm may match", dist: Dist{Integrity: otherSRI + " " + sri512}, wantVerified: true},
		{name: "strongest algorithm decides", dist: Dist{Integrity: sri256 + " " + otherSRI}, wantErr: ErrIntegrityMismatch},
		{name: "options are ignored", dist: Dist{Integrity: sri512 + "?foo=bar"}, wantVerified: true},
		{name: "wrong integrity", dist: Dist{Integrity: otherSRI}, wantErr: ErrIntegrityMismatch},
		{name: "shasum only", dist: Dist{Shasum: shasum}, wantVerified: true},
		{name: "shasum upper case", dist: Dist{Shasum: strings.ToUpper(shasum)}, wantVerified: true},
		{name: "wrong shasum", dist: Dist{Shasum: strings.Repeat("0", 40)}, wantErr: ErrIntegrityMismatch},
		{name: "malformed shasum", dist: Dist{Shasum: "zz"}, wantErr: ErrIntegrityMismatch},
		{name: "unknown algorithm falls back to shasum", dist: Dist{Integrity: "md5-AAAA", Shasum: shasum}, wantVerified: true},
		{name: "no digest", dist: Dist{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verified, err := VerifyDist(strings.NewReader(string(body)), tt.dist)
			if !errors.Is(err, tt.wantErr) || verified != tt.wantVerified {
				t.Errorf("VerifyDist = %v, %v; want %v, %v", verified, err, tt.wantVerified, tt.wantErr)
			}
		})
	}
}
