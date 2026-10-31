// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// ErrAllVersionsHidden is returned by HideVersions when it would hide every
// version of a packument that had some.
var ErrAllVersionsHidden = errors.New("every version of the packument is hidden")

// publishedAt reads one packument time entry: an RFC 3339 time. ok is false
// for an absent or unparsable entry.
func publishedAt(raw json.RawMessage) (time.Time, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}, false
	}

	t, err := time.Parse(time.RFC3339, s)

	return t, err == nil
}

// PublishedAt is the packument's time entry of version; ok is false when the
// packument has none for it or it is not an RFC 3339 time.
func PublishedAt(doc []byte, version string) (time.Time, bool, error) {
	top, err := parsePackument(doc)
	if err != nil {
		return time.Time{}, false, err
	}

	times, err := objectOf(top["time"])
	if err != nil {
		return time.Time{}, false, err
	}

	t, ok := publishedAt(times[version])

	return t, ok, nil
}

// HideVersions removes from a full packument every version hide reports true
// for. hide is asked about every version with its time entry (the zero time
// when the entry is absent or not an RFC 3339 time, so a rule that needs no
// date still applies): the version and its time entry go ("created" and
// "modified" stay), and a dist-tag that pointed at a removed version moves
// ("latest": to the highest remaining version that is not a prerelease, or
// the highest remaining one when only prereleases are left) or goes (every
// other tag). doc is returned
// as is when nothing is hidden; ErrAllVersionsHidden when nothing would be
// left of a packument that had versions.
func HideVersions(doc []byte, hide func(version string, published time.Time) bool) ([]byte, error) {
	top, err := parsePackument(doc)
	if err != nil {
		return nil, err
	}

	versions, err := objectOf(top["versions"])
	if err != nil {
		return nil, err
	}

	times, err := objectOf(top["time"])
	if err != nil {
		return nil, err
	}

	hidden := map[string]struct{}{}

	for ver := range versions {
		published, _ := publishedAt(times[ver])
		if hide(ver, published) {
			hidden[ver] = struct{}{}
		}
	}

	if len(hidden) == 0 {
		return doc, nil
	}

	if len(hidden) == len(versions) {
		return nil, ErrAllVersionsHidden
	}

	for ver := range hidden {
		delete(versions, ver)
		delete(times, ver)
	}

	if top["versions"], err = marshalObject(versions); err != nil {
		return nil, err
	}

	if top["time"], err = marshalObject(times); err != nil {
		return nil, err
	}

	if err := moveDistTags(top, versions, hidden); err != nil {
		return nil, err
	}

	return marshalPackument(top)
}

// moveDistTags repoints the dist-tags of top that name a hidden version.
func moveDistTags(top, versions map[string]json.RawMessage, hidden map[string]struct{}) error {
	tags, err := objectOf(top["dist-tags"])
	if err != nil {
		return err
	}

	changed := false

	for tag, raw := range tags {
		var ver string
		if json.Unmarshal(raw, &ver) != nil {
			continue
		}

		if _, gone := hidden[ver]; !gone {
			continue
		}

		changed = true

		best := ""
		if tag == "latest" {
			best = newestVersion(versions)
		}

		if best == "" {
			delete(tags, tag)
			continue
		}

		if tags[tag], err = json.Marshal(best); err != nil {
			return err //nolint:wrapcheck // a string always marshals
		}
	}

	if !changed {
		return nil
	}

	top["dist-tags"], err = marshalObject(tags)

	return err
}

// newestVersion is the highest valid semver among versions that is not a
// prerelease, else the highest valid one; "" when none is valid.
func newestVersion(versions map[string]json.RawMessage) string {
	var stable, highest string

	for ver := range versions {
		if ValidateSemver(ver) != nil {
			continue
		}

		if higher(ver, highest) {
			highest = ver
		}

		if semver.Prerelease("v"+ver) == "" && higher(ver, stable) {
			stable = ver
		}
	}

	if stable != "" {
		return stable
	}

	return highest
}

// higher reports whether version a orders after b ("" is below everything);
// equal precedence (build metadata only) falls back to string order so the
// answer does not depend on map order.
func higher(a, b string) bool {
	if b == "" {
		return true
	}

	if c := semver.Compare("v"+a, "v"+b); c != 0 {
		return c > 0
	}

	return strings.Compare(a, b) > 0
}

// VersionTime is one version of a packument with its time entry.
type VersionTime struct {
	Version string
	// Published is the zero time when the packument has no RFC 3339 time entry for it.
	Published time.Time
}

// Versions lists the versions of a full packument with their time entries, in
// no particular order.
func Versions(doc []byte) ([]VersionTime, error) {
	top, err := parsePackument(doc)
	if err != nil {
		return nil, err
	}

	versions, err := objectOf(top["versions"])
	if err != nil {
		return nil, err
	}

	times, err := objectOf(top["time"])
	if err != nil {
		return nil, err
	}

	out := make([]VersionTime, 0, len(versions))

	for v := range versions {
		t, _ := publishedAt(times[v])
		out = append(out, VersionTime{Version: v, Published: t})
	}

	return out, nil
}
