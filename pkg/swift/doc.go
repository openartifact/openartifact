// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MaxDocBytes is the largest registry JSON document a proxy accepts from an
// upstream.
const MaxDocBytes = 32 << 20

// ErrInvalidDoc is wrapped by every error of the Parse functions.
var ErrInvalidDoc = errors.New("invalid registry document")

// SourceArchive is the name of the resource that holds the source archive of a
// release.
const SourceArchive = "source-archive"

// Problem is the RFC 7807 problem details object of a registry response.
type Problem struct {
	Status int    `json:"status,omitempty"`
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail"`
}

// ReleaseEntry is one release of a package in the list document. URL is
// optional: a client builds it from the version when it is absent.
type ReleaseEntry struct {
	URL     string   `json:"url,omitempty"`
	Problem *Problem `json:"problem,omitempty"`
}

// ReleaseList is the document of GET /{scope}/{name}.
type ReleaseList struct {
	Releases map[string]ReleaseEntry `json:"releases"`
}

// Versions returns the versions of the list, newest first (highest precedence
// first, the order the specification asks for).
func (l ReleaseList) Versions() []string {
	vs := make([]string, 0, len(l.Releases))
	for v := range l.Releases {
		vs = append(vs, v)
	}

	SortNewestFirst(vs)

	return vs
}

// SortNewestFirst sorts versions by descending precedence.
func SortNewestFirst(vs []string) {
	slices.SortFunc(vs, func(a, b string) int { return Compare(b, a) })
}

// Signing is the signature of a resource.
type Signing struct {
	SignatureBase64Encoded string `json:"signatureBase64Encoded"` //nolint:tagliatelle // Swift Package Registry and stored-document wire format
	SignatureFormat        string `json:"signatureFormat"`        //nolint:tagliatelle // Swift Package Registry and stored-document wire format
}

// Resource is one downloadable resource of a release.
type Resource struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Checksum string   `json:"checksum"`
	Signing  *Signing `json:"signing,omitempty"`
}

// Release is the document of GET /{scope}/{name}/{version}. Metadata is the
// metadata JSON object the release was published with, verbatim.
type Release struct {
	ID          string          `json:"id"`
	Version     string          `json:"version"`
	Resources   []Resource      `json:"resources"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	PublishedAt string          `json:"publishedAt,omitempty"` //nolint:tagliatelle // Swift Package Registry and stored-document wire format
}

// Checksum is the lower-case hexadecimal SHA-256 of the source archive, "" when
// the document has no source-archive resource.
func (r Release) Checksum() string {
	for _, res := range r.Resources {
		if res.Name == SourceArchive {
			return res.Checksum
		}
	}

	return ""
}

// ParseReleaseList reads the release list an upstream registry sent. Releases
// under an invalid version are dropped; a document that is not a JSON object
// with a "releases" object is an error.
func ParseReleaseList(data []byte) (ReleaseList, error) {
	var raw struct {
		Releases map[string]json.RawMessage `json:"releases"`
	}

	if err := strictObject(data, &raw); err != nil {
		return ReleaseList{}, err
	}

	if raw.Releases == nil {
		return ReleaseList{}, fmt.Errorf(`%w: no "releases"`, ErrInvalidDoc)
	}

	list := ReleaseList{Releases: make(map[string]ReleaseEntry, len(raw.Releases))}

	for v, entry := range raw.Releases {
		if !ValidVersion(v) {
			continue
		}

		var e ReleaseEntry

		if len(entry) > 0 && json.Unmarshal(entry, &e) != nil {
			e = ReleaseEntry{}
		}

		list.Releases[v] = ReleaseEntry{URL: e.URL}
	}

	return list, nil
}

// ParseRelease reads the release document an upstream sent and checks it
// describes release id at version: the identity and version match (identities
// compare case-insensitively) and the source archive has a SHA-256 checksum.
func ParseRelease(data []byte, id, version string) (Release, error) {
	var rel Release

	if err := strictObject(data, &rel); err != nil {
		return Release{}, err
	}

	switch {
	case !equalFold(rel.ID, id):
		return Release{}, fmt.Errorf("%w: id %q, want %q", ErrInvalidDoc, rel.ID, id)
	case rel.Version != version:
		return Release{}, fmt.Errorf("%w: version %q, want %q", ErrInvalidDoc, rel.Version, version)
	case !ValidSHA256Hex(rel.Checksum()):
		return Release{}, fmt.Errorf("%w: no source-archive SHA-256 checksum", ErrInvalidDoc)
	}

	return rel, nil
}

// ValidSHA256Hex reports whether s is 64 lower-case hexadecimal digits.
func ValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}

	for i := range len(s) {
		if c := s[i]; c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}

	return true
}

// DigestHeader is the value of the Digest header of a source archive whose
// SHA-256 is the hexadecimal sum: "sha-256=" and the base64 of the raw digest.
func DigestHeader(sumHex string) string {
	raw, err := hex.DecodeString(sumHex)
	if err != nil {
		return ""
	}

	return "sha-256=" + base64.StdEncoding.EncodeToString(raw)
}

func equalFold(a, b string) bool {
	return strings.EqualFold(a, b)
}

// strictObject unmarshals data, which must be a JSON object, into v.
func strictObject(data []byte, v any) error {
	if len(data) > MaxDocBytes {
		return fmt.Errorf("%w: too large", ErrInvalidDoc)
	}

	if t := bytes.TrimSpace(data); len(t) == 0 || t[0] != '{' {
		return fmt.Errorf("%w: not a JSON object", ErrInvalidDoc)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDoc, err)
	}

	return nil
}
