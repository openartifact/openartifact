// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift_test

import (
	"testing"

	"github.com/openartifact/openartifact/pkg/swift"
)

func TestLatest(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		versions []string
		want     string
	}{
		{nil, ""},
		{[]string{"2.0.0-rc.1", "1.1.0", "1.0.0"}, "1.1.0"},
		{[]string{"2.0.0-rc.1", "2.0.0-beta.2"}, "2.0.0-rc.1"},
		{[]string{"1.0.0+build-5", "1.0.0"}, "1.0.0+build-5"},
	} {
		if got := swift.Latest(c.versions); got != c.want {
			t.Errorf("Latest(%v) = %q, want %q", c.versions, got, c.want)
		}
	}
}

func TestListBody_KeepsTheOrderAndParses(t *testing.T) {
	t.Parallel()

	body := swift.ListBody([]string{"2.0.0", "1.0.0"}, func(v string) string { return "https://r.example/mona/Lib/" + v })

	const want = `{"releases":{"2.0.0":{"url":"https://r.example/mona/Lib/2.0.0"},"1.0.0":{"url":"https://r.example/mona/Lib/1.0.0"}}}`
	if string(body) != want {
		t.Errorf("body = %s, want %s", body, want)
	}

	list, err := swift.ParseReleaseList(body)
	if err != nil || len(list.Releases) != 2 {
		t.Fatalf("ParseReleaseList = %+v, %v", list, err)
	}

	if got := string(swift.ListBody([]string{"1.0.0"}, nil)); got != `{"releases":{"1.0.0":{}}}` {
		t.Errorf("without URLs = %s", got)
	}
}

func TestParseTarget(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		url  string
		want swift.Target
		ok   bool
	}{
		{"https://r.example/repos/swift/main/mona/LinkedList/1.1.0", swift.Target{Version: "1.1.0"}, true},
		{"/repos/swift/main/MONA/linkedlist/1.1.0-rc.1", swift.Target{Version: "1.1.0-rc.1"}, true},
		{"https://r.example/mona/LinkedList/1.1.0/Package.swift", swift.Target{Version: "1.1.0", Manifest: true}, true},
		{
			"https://r.example/a/mona/LinkedList/1.1.0/Package.swift?swift-version=5.4",
			swift.Target{Version: "1.1.0", Manifest: true, SwiftVersion: "5.4"}, true,
		},
		{"https://r.example/mona/LinkedList/1.1.0.zip", swift.Target{}, false},
		{"https://r.example/mona/Other/1.1.0", swift.Target{}, false},
		{"https://r.example/mona/LinkedList", swift.Target{}, false},
		{"https://r.example/mona/LinkedList/1.1.0/other.swift", swift.Target{}, false},
		{"https://r.example/mona/LinkedList/latest", swift.Target{}, false},
		{"://bad", swift.Target{}, false},
	} {
		got, ok := swift.ParseTarget(c.url, "mona", "LinkedList")
		if ok != c.ok || got != c.want {
			t.Errorf("ParseTarget(%q) = %+v, %v; want %+v, %v", c.url, got, ok, c.want, c.ok)
		}
	}
}
