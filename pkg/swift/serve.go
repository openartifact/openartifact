// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
)

// Latest picks the release a registry names with a "latest-version" link from
// versions, newest first: the newest one that is not a pre-release, or the
// newest of all when every one is. It returns "" for no versions.
func Latest(versions []string) string {
	for _, v := range versions {
		if !prerelease(v) {
			return v
		}
	}

	if len(versions) == 0 {
		return ""
	}

	return versions[0]
}

// prerelease reports whether a SemVer version has a pre-release part.
func prerelease(v string) bool {
	core, _, _ := strings.Cut(v, "+")

	return strings.Contains(core, "-")
}

// ListBody renders the release list document of a package: the versions in the
// order given (newest first), each with the release URL releaseURL returns for
// it. A nil releaseURL leaves the URLs out, which the protocol allows.
func ListBody(versions []string, releaseURL func(version string) string) []byte {
	var body bytes.Buffer

	body.WriteString(`{"releases":{`)

	for i, v := range versions {
		if i > 0 {
			body.WriteByte(',')
		}

		// A string and a struct of strings always encode.
		key, _ := json.Marshal(v) //nolint:errchkjson // cannot fail

		var entry ReleaseEntry
		if releaseURL != nil {
			entry.URL = releaseURL(v)
		}

		value, _ := json.Marshal(entry) //nolint:errchkjson // cannot fail

		body.Write(key)
		body.WriteByte(':')
		body.Write(value)
	}

	body.WriteString(`}}`)

	return body.Bytes()
}

// Target is what a link of a registry response points at within a package: the
// release Version or, with Manifest, its Package.swift (SwiftVersion names the
// version-specific manifest, "" the plain one).
type Target struct {
	Version      string
	Manifest     bool
	SwiftVersion string
}

// ParseTarget reads a link target of package scope/name as another registry
// wrote it: any absolute or relative URL whose path ends with
// "/{scope}/{name}/{version}" or "/{scope}/{name}/{version}/Package.swift". The
// URL's own prefix is not looked at, so links of a registry mounted anywhere are
// understood; scope and name compare case-insensitively. ok is false for every
// other URL.
func ParseTarget(rawURL, scope, name string) (Target, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Target{}, false
	}

	segs := strings.Split(strings.Trim(u.Path, "/"), "/")

	for i := len(segs) - 3; i >= 0; i-- {
		if !strings.EqualFold(segs[i], scope) || !strings.EqualFold(segs[i+1], name) {
			continue
		}

		rest := segs[i+2:]
		if !ValidVersion(rest[0]) {
			return Target{}, false
		}

		switch {
		case len(rest) == 1:
			return Target{Version: rest[0]}, true
		case len(rest) == 2 && rest[1] == ManifestFile:
			return Target{Version: rest[0], Manifest: true, SwiftVersion: u.Query().Get("swift-version")}, true
		}

		return Target{}, false
	}

	return Target{}, false
}
