// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const upstreamDoc = `{
  "name": "http",
  "isDiscontinued": false,
  "latest": {"version": "1.1.0"},
  "versions": [
    {"version": "1.0.0", "archive_url": "https://pub.dev/api/archives/http-1.0.0.tar.gz",
     "archive_sha256": "` + sum1 + `", "published": "2023-01-02T03:04:05.678901Z", "pubspec": {"name": "http", "version": "1.0.0"}},
    {"version": "1.1.0-beta", "archive_url": "https://pub.dev/api/archives/http-1.1.0-beta.tar.gz", "pubspec": {}},
    {"version": "1.1.0", "retracted": true, "archive_url": "https://pub.dev/api/archives/http-1.1.0.tar.gz", "pubspec": {}},
    {"version": "1.0.0", "archive_url": "https://evil.example/dup.tar.gz", "pubspec": {}},
    {"version": "bad", "archive_url": "https://pub.dev/x", "pubspec": {}},
    {"version": "2.0.0", "pubspec": {}},
    {"version": "2.0.1", "archive_url": "javascript:alert(1)", "pubspec": {}}
  ],
  "future_field": [1, 2]
}`

const sum1 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type parsedDoc struct {
	Name     string                       `json:"name"`
	Latest   map[string]json.RawMessage   `json:"latest"`
	Versions []map[string]json.RawMessage `json:"versions"`
	Future   []int                        `json:"future_field"`
}

func link(v *Version) string {
	return "https://oa.example/repos/pub/r/" + ArchivePath("http", v.Version)
}

func TestParseDocDropsUnusableEntries(t *testing.T) {
	t.Parallel()

	d, err := ParseDoc([]byte(upstreamDoc))
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, v := range d.Versions {
		got = append(got, v.Version)
	}

	if want := "1.0.0 1.1.0-beta 1.1.0"; strings.Join(got, " ") != want {
		t.Fatalf("versions = %v, want %s", got, want)
	}

	first := d.Find("1.0.0")
	if first.SHA256 != sum1 || first.Published.Year() != 2023 || first.ArchiveURL != "https://pub.dev/api/archives/http-1.0.0.tar.gz" {
		t.Errorf("first entry = %+v", first)
	}

	if !d.Find("1.1.0").Retracted {
		t.Error("the retraction is lost")
	}
}

func TestMarshalRewritesArchiveURLsAndRecomputesLatest(t *testing.T) {
	t.Parallel()

	d, err := ParseDoc([]byte(upstreamDoc))
	if err != nil {
		t.Fatal(err)
	}

	body, err := d.Marshal(link, nil)
	if err != nil {
		t.Fatal(err)
	}

	var out parsedDoc
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}

	if out.Name != "http" || len(out.Versions) != 3 || len(out.Future) != 2 {
		t.Fatalf("document = %s", body)
	}

	for _, v := range out.Versions {
		if !strings.HasPrefix(string(v["archive_url"]), `"https://oa.example/repos/pub/r/api/archives/http-`) {
			t.Errorf("archive_url not rewritten: %s", v["archive_url"])
		}
	}

	// 1.1.0 is retracted, so the latest stable is 1.0.0, not the pre-release.
	if string(out.Latest["version"]) != `"1.0.0"` {
		t.Errorf("latest = %s", out.Latest["version"])
	}

	if string(out.Versions[0]["pubspec"]) != `{"name":"http","version":"1.0.0"}` &&
		string(out.Versions[0]["pubspec"]) != `{"name": "http", "version": "1.0.0"}` {
		t.Errorf("pubspec not kept: %s", out.Versions[0]["pubspec"])
	}

	// Hiding the stable release makes the pre-release the latest.
	body, err = d.Marshal(link, func(v *Version) bool { return v.Version == "1.1.0-beta" })
	if err != nil {
		t.Fatal(err)
	}

	out = parsedDoc{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}

	if len(out.Versions) != 1 || string(out.Latest["version"]) != `"1.1.0-beta"` {
		t.Errorf("filtered document = %s", body)
	}
}

func TestMergeFirstWinsAndSorts(t *testing.T) {
	t.Parallel()

	hosted := NewDoc("http", []*Version{
		NewVersion("1.0.0", "http://hosted/1", sum1, time.Time{}, json.RawMessage(`{"from":"hosted"}`)),
		NewVersion("0.5.0", "http://hosted/05", "", time.Time{}, nil),
	})

	upstream, err := ParseDoc([]byte(upstreamDoc))
	if err != nil {
		t.Fatal(err)
	}

	merged := Merge([]*Doc{hosted, upstream})

	var got []string
	for _, v := range merged.Versions {
		got = append(got, v.Version)
	}

	if want := "0.5.0 1.0.0 1.1.0-beta 1.1.0"; strings.Join(got, " ") != want {
		t.Fatalf("merged = %v, want %s", got, want)
	}

	if merged.Find("1.0.0").ArchiveURL != "http://hosted/1" {
		t.Error("the first document does not win a shared version")
	}

	if Merge(nil) != nil {
		t.Error("Merge of nothing is not nil")
	}
}

func TestParseDocRejects(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string]string{
		"not json":     "<html>",
		"no name":      `{"versions": []}`,
		"bad name":     `{"name": "Bad-Name", "versions": []}`,
		"no versions":  `{"name": "http"}`,
		"versions obj": `{"name": "http", "versions": {}}`,
	} {
		if _, err := ParseDoc([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if err := CheckDoc(strings.NewReader(upstreamDoc), "other"); err == nil {
		t.Error("CheckDoc accepted the document of another package")
	}

	if err := CheckDoc(strings.NewReader(upstreamDoc), "http"); err != nil {
		t.Errorf("CheckDoc: %v", err)
	}
}

func TestLatestPreference(t *testing.T) {
	t.Parallel()

	mk := func(v string, retracted bool) *Version { return &Version{Version: v, Retracted: retracted} }

	if Latest(nil) != nil {
		t.Error("Latest of nothing is not nil")
	}

	if got := Latest([]*Version{mk("1.0.0", true), mk("2.0.0", true)}); got.Version != "2.0.0" {
		t.Errorf("all retracted: %s", got.Version)
	}

	if got := Latest([]*Version{mk("1.0.0", false), mk("2.0.0-rc.1", false)}); got.Version != "1.0.0" {
		t.Errorf("stable preferred: %s", got.Version)
	}
}
