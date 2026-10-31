// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"crypto/sha1" //nolint:gosec // Composer's dist "shasum" is a SHA-1 by protocol; a corruption check, not authentication
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"
)

// ErrDocument is a p2 document that cannot be used: not JSON, without the
// requested package or without a usable version.
var ErrDocument = errors.New("unusable composer metadata")

// ErrChecksum is a dist whose bytes do not match the shasum its source published.
var ErrChecksum = errors.New("dist does not match its shasum")

// distTypeZip is the only dist type a repository of this server serves.
const distTypeZip = "zip"

// VersionString is the "version" of an entry; "" when it has none.
func (v Version) VersionString() string {
	s, _ := v["version"].(string)
	return s
}

// ZipDist is the zip dist block of an entry as its source published it. It is
// false for an entry without a dist, with one of another type (a tarball) or
// with an URL that is not an absolute http(s) one.
func (v Version) ZipDist() (Dist, bool) {
	block, ok := v["dist"].(map[string]any)
	if !ok {
		return Dist{}, false
	}

	field := func(k string) string {
		s, _ := block[k].(string)
		return s
	}

	d := Dist{URL: field("url"), SHA1: strings.ToLower(field("shasum")), Reference: field("reference")}

	if !strings.EqualFold(field("type"), distTypeZip) || !httpURL(d.URL) {
		return Dist{}, false
	}

	return d, true
}

// httpURL reports whether s looks like an absolute http(s) URL.
func httpURL(s string) bool {
	rest, ok := strings.CutPrefix(s, "https://")
	if !ok {
		rest, ok = strings.CutPrefix(s, "http://")
	}

	return ok && rest != "" && rest[0] != '/'
}

// ProxyRef is the content reference a proxy repository gives the dist: a digest
// of where the upstream serves it from and of the reference it names, so the URL
// changes whenever either does and a client can never be handed one version's
// bytes under another's URL.
func (d Dist) ProxyRef() string {
	sum := sha256.New()
	sum.Write([]byte(d.URL))
	sum.Write([]byte{0})
	sum.Write([]byte(d.Reference))

	return hex.EncodeToString(sum.Sum(nil))
}

// ParseDocument decodes a p2 document and returns the full entries of package
// name. Entries that are not objects or have no usable "version" are dropped;
// ErrDocument is returned when the document is not JSON, lacks the package or
// leaves no entry.
func ParseDocument(data []byte, name string) ([]Version, error) {
	pkgs, err := Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDocument, err)
	}

	entries := pkgs[name]
	out := make([]Version, 0, len(entries))

	for _, v := range entries {
		if s := v.VersionString(); s != "" && len(s) <= MaxVersionLen {
			out = append(out, v)
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no version of %s", ErrDocument, name)
	}

	return out, nil
}

// FindVersion returns the entry whose "version" is exactly version.
func FindVersion(versions []Version, version string) (Version, bool) {
	for _, v := range versions {
		if v.VersionString() == version {
			return v, true
		}
	}

	return nil, false
}

// RelinkSource points the dists of entries a source other than this server
// published at repository root (the absolute URL of a proxy repository, without
// a trailing slash). The reference and shasum stay the source's, so Composer
// still verifies what it downloads against them. An entry whose dist cannot be
// proxied (no dist, another type, no http(s) URL) loses it and is installed from
// its "source" by Composer. The entries are modified.
func RelinkSource(versions []Version, name, root string) {
	for _, v := range versions {
		if _, had := v["dist"]; !had {
			continue
		}

		d, ok := v.ZipDist()
		if !ok {
			delete(v, "dist")
			continue
		}

		v["dist"] = map[string]any{
			"type":      distTypeZip,
			"url":       root + "/" + DistPath(name, v.VersionString(), d.ProxyRef()),
			"reference": d.Reference,
			"shasum":    d.SHA1,
		}
	}
}

// RelinkMember points the dists of entries a repository of this server served
// at repository root: the file name of the dist (its content reference) is kept
// because the member keeps serving it under the same path. An entry whose dist
// is none of a repository's loses it. The entries are modified.
func RelinkMember(versions []Version, name, root string) {
	for _, v := range versions {
		block, ok := v["dist"].(map[string]any)
		if !ok {
			delete(v, "dist")
			continue
		}

		u, _ := block["url"].(string)

		ref, ok := DistRef(u)
		if !ok {
			delete(v, "dist")
			continue
		}

		relinked := make(map[string]any, len(block))
		maps.Copy(relinked, block)

		relinked["url"] = root + "/" + DistPath(name, v.VersionString(), ref)
		v["dist"] = relinked
	}
}

// ErrNoneLeft is a document whose every version was filtered out.
var ErrNoneLeft = errors.New("no version left")

// Published is when the entry's source published the version ("time"); the zero
// time when it states none or one that does not parse.
func (v Version) Published() time.Time {
	s, _ := v["time"].(string)
	if s == "" {
		return time.Time{}
	}

	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}

	return t
}

// Filter returns the entries of versions for which keep is true, in order.
func Filter(versions []Version, keep func(Version) bool) []Version {
	out := make([]Version, 0, len(versions))

	for _, v := range versions {
		if keep(v) {
			out = append(out, v)
		}
	}

	return out
}

// RenderSource turns the p2 document of package name as a source other than
// this server published it into the document a proxy repository at root serves:
// only that package, only the versions keep accepts (nil keeps all), with its
// dists relinked, minified. The filter sees the expanded entries, so a version
// that inherited its fields in the minified upstream document is judged by what
// it inherited. ErrNoneLeft when keep rejects every version.
func RenderSource(data []byte, name, root string, keep func(Version) bool) ([]byte, error) {
	versions, err := ParseDocument(data, name)
	if err != nil {
		return nil, err
	}

	if keep != nil {
		if versions = Filter(versions, keep); len(versions) == 0 {
			return nil, ErrNoneLeft
		}
	}

	RelinkSource(versions, name, root)

	return EncodeDocument(map[string][]Version{name: versions}, true)
}

// MergeKey identifies a version across repositories: its normalized form.
func (v Version) MergeKey() string {
	if n, ok := v["version_normalized"].(string); ok && n != "" {
		return n
	}

	if n, err := Normalize(v.VersionString()); err == nil {
		return n
	}

	return v.VersionString()
}

// VerifySHA1 reads r and checks it against the hex SHA-1 a source published as
// the "shasum" of a dist. An empty want (Packagist publishes none for most
// dists) checks nothing and reads nothing.
func VerifySHA1(r io.Reader, want string) error {
	if want == "" {
		return nil
	}

	h := sha1.New() //nolint:gosec // see the import
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("hash dist: %w", err)
	}

	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		return ErrChecksum
	}

	return nil
}
