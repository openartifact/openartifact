// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"strings"
	"testing"
	"time"
)

func TestBuildTimes(t *testing.T) {
	const doc = `{"info":{"subdir":"linux-64"},
"packages":{
 "demo-1.0-0.tar.bz2":{"name":"demo","version":"1.0","build":"0","timestamp":1700000200},
 "demo-1.0-1.tar.bz2":{"name":"demo","version":"1.0","build":"1","timestamp":1700000100},
 "demo-2.0-0.tar.bz2":{"name":"demo","version":"2.0","build":"0"},
 "other-1.0-0.tar.bz2":{"name":"other","version":"1.0","build":"0","timestamp":1600000000}},
"packages.conda":{
 "demo-1.0-2.conda":{"name":"Demo","version":"1.0","build":"2","timestamp":1700000300000},
 "demo-3.0-0.conda":{"name":"demo","version":"3.0","build":"0","timestamp":1800000000}}}`

	got, err := BuildTimes(strings.NewReader(doc), 1<<20, "DEMO")
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]time.Time{
		"1.0": time.Unix(1700000100, 0).UTC(), // earliest of three builds, in seconds and ms
		"2.0": {},                             // untimed
		"3.0": time.Unix(1800000000, 0).UTC(),
	}

	if len(got) != len(want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}

	for v, w := range want {
		if g, ok := got[v]; !ok || !g.Equal(w) {
			t.Errorf("version %s = %v (present %v), want %v", v, g, ok, w)
		}
	}
}
