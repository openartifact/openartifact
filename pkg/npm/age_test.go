// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type agePack struct {
	DistTags map[string]string          `json:"dist-tags"` //nolint:tagliatelle // npm wire format
	Versions map[string]json.RawMessage `json:"versions"`
	Time     map[string]string          `json:"time"`
}

// ageHide hides what was published at or after cutoff.
func ageHide(cutoff string) func(string, time.Time) bool {
	c, err := time.Parse(time.RFC3339, cutoff)
	if err != nil {
		panic(err)
	}

	return func(_ string, p time.Time) bool { return !p.IsZero() && !p.Before(c) }
}

func TestHideVersions(t *testing.T) {
	const doc = `{"name":"p","dist-tags":{"latest":"2.0.0","next":"3.0.0-rc.1","old":"1.0.0"},
	  "versions":{"1.0.0":{"v":1},"1.5.0":{"v":15},"2.0.0":{"v":2},"3.0.0-rc.1":{"v":3}},
	  "time":{"created":"2020-01-01T00:00:00.000Z","modified":"2020-06-01T00:00:00.000Z",
	    "1.0.0":"2020-01-01T00:00:00.000Z","1.5.0":"2020-02-01T00:00:00.000Z",
	    "2.0.0":"2020-03-01T00:00:00.000Z","3.0.0-rc.1":"2020-04-01T00:00:00.000Z"}}`

	tests := []struct {
		name     string
		cutoff   string
		versions []string
		tags     map[string]string
	}{
		{"nothing hidden", "2021-01-01T00:00:00Z", []string{"1.0.0", "1.5.0", "2.0.0", "3.0.0-rc.1"},
			map[string]string{"latest": "2.0.0", "next": "3.0.0-rc.1", "old": "1.0.0"}},
		{"exactly at the cutoff is hidden", "2020-03-01T00:00:00Z", []string{"1.0.0", "1.5.0"},
			map[string]string{"latest": "1.5.0", "old": "1.0.0"}},
		{"one second before the cutoff is kept", "2020-03-01T00:00:01Z", []string{"1.0.0", "1.5.0", "2.0.0"},
			map[string]string{"latest": "2.0.0", "old": "1.0.0"}},
		{"prerelease only hidden", "2020-04-01T00:00:00Z", []string{"1.0.0", "1.5.0", "2.0.0"},
			map[string]string{"latest": "2.0.0", "old": "1.0.0"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := HideVersions([]byte(doc), ageHide(tt.cutoff))
			if err != nil {
				t.Fatal(err)
			}

			var got agePack
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}

			vers := make([]string, 0, len(got.Versions))
			for v := range got.Versions {
				vers = append(vers, v)
			}

			slices.Sort(vers)

			if !slices.Equal(vers, tt.versions) {
				t.Errorf("versions = %v, want %v", vers, tt.versions)
			}

			for v := range got.Time {
				if _, ok := got.Versions[v]; !ok && v != "created" && v != "modified" {
					t.Errorf("time still lists removed version %s", v)
				}
			}

			if got.Time["created"] == "" || got.Time["modified"] == "" {
				t.Errorf("created/modified dropped: %v", got.Time)
			}

			if len(got.DistTags) != len(tt.tags) {
				t.Errorf("dist-tags = %v, want %v", got.DistTags, tt.tags)
			}

			for tag, want := range tt.tags {
				if got.DistTags[tag] != want {
					t.Errorf("dist-tags = %v, want %v", got.DistTags, tt.tags)
				}
			}
		})
	}
}

func TestHideVersionsPrereleaseFallbackAndOtherTagsDropped(t *testing.T) {
	const doc = `{"dist-tags":{"latest":"1.0.0","beta":"1.0.0"},
	  "versions":{"1.0.0":{},"1.1.0-beta.1":{},"1.1.0-beta.2":{}},
	  "time":{"1.0.0":"2020-03-01T00:00:00Z","1.1.0-beta.1":"2020-01-01T00:00:00Z","1.1.0-beta.2":"2020-02-01T00:00:00Z"}}`

	out, err := HideVersions([]byte(doc), ageHide("2020-03-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}

	var got agePack
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	if got.DistTags["latest"] != "1.1.0-beta.2" {
		t.Errorf("latest = %q, want the highest prerelease left", got.DistTags["latest"])
	}

	if _, ok := got.DistTags["beta"]; ok {
		t.Errorf("beta tag kept: %v", got.DistTags)
	}
}

func TestHideVersionsAsksEveryVersionWithItsTimeOrZero(t *testing.T) {
	const doc = `{"dist-tags":{"latest":"2.0.0"},"versions":{"1.0.0":{},"2.0.0":{}},"time":{"2.0.0":"not a time"}}`

	seen := map[string]bool{}

	out, err := HideVersions([]byte(doc), func(v string, p time.Time) bool {
		seen[v] = p.IsZero()
		return false
	})
	if err != nil || string(out) != doc {
		t.Errorf("nothing hidden: %s, %v; want the document unchanged", out, err)
	}

	if len(seen) != 2 || !seen["1.0.0"] || !seen["2.0.0"] {
		t.Errorf("hide saw %v; want both versions with a zero time", seen)
	}

	out, err = HideVersions([]byte(doc), func(v string, _ time.Time) bool { return v == "2.0.0" })
	if err != nil || strings.Contains(string(out), `"2.0.0":{`) || !strings.Contains(string(out), `"latest":"1.0.0"`) {
		t.Errorf("hiding an undated version by name: %s, %v", out, err)
	}

	const all = `{"versions":{"1.0.0":{},"2.0.0":{}},"time":{"1.0.0":"2020-01-01T00:00:00Z","2.0.0":"2020-01-02T00:00:00Z"}}`
	if _, err := HideVersions([]byte(all), func(string, time.Time) bool { return true }); !errors.Is(err, ErrAllVersionsHidden) {
		t.Errorf("all hidden: err = %v, want ErrAllVersionsHidden", err)
	}

	// A packument without versions is not "all hidden".
	out, err = HideVersions([]byte(`{"name":"p"}`), func(string, time.Time) bool { return true })
	if err != nil || !strings.Contains(string(out), `"p"`) {
		t.Errorf("no versions: %s, %v", out, err)
	}
}
