// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"crypto/sha1" //nolint:gosec // dist.shasum is SHA-1 by protocol
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/openartifact/openartifact/internal/pathseg"
)

// This file holds the registry-protocol helpers shared by proxy and group
// repositories: request path classification, packument tarball rewriting,
// packument merging and dist integrity verification. They operate on
// packuments as opaque JSON so fields they do not know survive untouched.

// AbbreviatedContentType is the media type of the install-v1 (abbreviated)
// packument.
const AbbreviatedContentType = "application/vnd.npm.install-v1+json"

// UpstreamAccept is the Accept header OpenArtifact sends upstream for an
// abbreviated packument (the value npm itself sends).
const UpstreamAccept = AbbreviatedContentType + "; q=1.0, application/json; q=0.8, */*"

// PathKind classifies a registry request path.
type PathKind int

const (
	// KindPackument is GET /<pkg>.
	KindPackument PathKind = iota + 1
	// KindVersion is GET /<pkg>/<version-or-tag>.
	KindVersion
	// KindTarball is GET /<pkg>/-/<file>.
	KindTarball
)

// Path is a parsed repository-relative registry path.
type Path struct {
	Kind PathKind
	// Package is the decoded package name ("lodash", "@scope/name").
	Package string
	// Version is the version or dist-tag of a KindVersion path.
	Version string
	// File is the tarball filename of a KindTarball path.
	File string
}

// ErrNotObject reports a packument that is not a JSON object.
var ErrNotObject = errors.New("npm packument is not a JSON object")

// ErrInvalidPath reports a path that is not a registry packument, version or
// tarball path.
var ErrInvalidPath = errors.New("not an npm registry path")

// ParsePath classifies a repository-relative path. The scope separator may be
// a literal "/" or an escaped %2f (npm sends "@scope%2fname" for packuments).
func ParsePath(rel string) (Path, error) {
	rel = strings.TrimPrefix(rel, "/")
	rel = strings.ReplaceAll(rel, "%2f", "/")
	rel = strings.ReplaceAll(rel, "%2F", "/")

	segs := strings.Split(rel, "/")

	pkg, rest := segs[0], segs[1:]

	if strings.HasPrefix(pkg, "@") {
		if len(rest) == 0 {
			return Path{}, ErrInvalidPath
		}

		pkg, rest = pkg+"/"+rest[0], rest[1:]
	}

	if !validRemoteName(pkg) {
		return Path{}, ErrInvalidPath
	}

	switch {
	case len(rest) == 0:
		return Path{Kind: KindPackument, Package: pkg}, nil
	case len(rest) == 1 && rest[0] != "-" && pathseg.Name(rest[0]):
		return Path{Kind: KindVersion, Package: pkg, Version: rest[0]}, nil
	case len(rest) == 2 && rest[0] == "-" && validFilename(rest[1]):
		return Path{Kind: KindTarball, Package: pkg, File: rest[1]}, nil
	}

	return Path{}, ErrInvalidPath
}

// validRemoteName is a deliberately lenient package-name check for upstream
// names: legacy registry packages may use upper case, which
// ValidatePackageName (publish rules) rejects. It only guarantees the name is
// one or two clean path segments that cannot alter an upstream URL.
func validRemoteName(name string) bool {
	if len(name) == 0 || len(name) > 214 {
		return false
	}

	scope, rest, scoped := strings.Cut(name, "/")
	if !scoped {
		return validRemoteSegment(name, false)
	}

	return strings.HasPrefix(scope, "@") && validRemoteSegment(scope[1:], false) && validRemoteSegment(rest, false)
}

func validRemoteSegment(seg string, allowEmpty bool) bool {
	if seg == "" {
		return allowEmpty
	}

	if seg[0] == '.' || seg[0] == '_' || seg[0] == '-' {
		return false
	}

	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '~':
		default:
			return false
		}
	}

	return true
}

func validFilename(f string) bool {
	return pathseg.Name(f) && !strings.ContainsFunc(f, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// WantsAbbreviated reports whether the Accept header asks for the install-v1
// (abbreviated) packument.
func WantsAbbreviated(accept string) bool {
	return strings.Contains(accept, AbbreviatedContentType)
}

// TarballFileOf returns the filename of an upstream dist.tarball URL.
func TarballFileOf(tarballURL string) string {
	u, err := url.Parse(tarballURL)
	if err != nil {
		return ""
	}

	f := path.Base(u.Path)
	if !validFilename(f) {
		return ""
	}

	return f
}

// parsePackument decodes a packument into its top-level members.
func parsePackument(doc []byte) (map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return nil, fmt.Errorf("parse packument: %w", err)
	}

	if top == nil {
		return nil, ErrNotObject
	}

	return top, nil
}

func objectOf(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse object: %w", err)
	}

	return m, nil
}

func marshalObject(m map[string]json.RawMessage) (json.RawMessage, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal object: %w", err)
	}

	return b, nil
}

// RewriteTarballs returns doc with every version's dist.tarball replaced by
// tarballBase + "/" + pkg + "/-/" + <filename of the old URL>, where
// tarballBase is "<server>/repos/<repo>". Integrity fields are untouched.
func RewriteTarballs(doc []byte, pkg, tarballBase string) ([]byte, error) {
	top, err := parsePackument(doc)
	if err != nil {
		return nil, err
	}

	if err := rewriteVersions(top, pkg, tarballBase); err != nil {
		return nil, err
	}

	return marshalPackument(top)
}

func marshalPackument(top map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("marshal packument: %w", err)
	}

	return b, nil
}

func rewriteVersions(top map[string]json.RawMessage, pkg, tarballBase string) error {
	versions, err := objectOf(top["versions"])
	if err != nil {
		return err
	}

	for ver, raw := range versions {
		manifest, err := objectOf(raw)
		if err != nil {
			return err
		}

		dist, err := objectOf(manifest["dist"])
		if err != nil {
			return err
		}

		if len(dist) == 0 {
			continue
		}

		var old string
		if err := json.Unmarshal(dist["tarball"], &old); err != nil && len(dist["tarball"]) > 0 {
			return fmt.Errorf("parse dist.tarball: %w", err)
		}

		file := TarballFileOf(old)
		if file == "" {
			file = pkg[strings.LastIndexByte(pkg, '/')+1:] + "-" + ver + ".tgz"
		}

		newURL, err := json.Marshal(strings.TrimRight(tarballBase, "/") + "/" + pkg + "/-/" + file)
		if err != nil {
			return fmt.Errorf("marshal tarball url: %w", err)
		}

		dist["tarball"] = newURL

		if manifest["dist"], err = marshalObject(dist); err != nil {
			return err
		}

		if versions[ver], err = marshalObject(manifest); err != nil {
			return err
		}
	}

	if len(versions) == 0 {
		return nil
	}

	top["versions"], err = marshalObject(versions)

	return err
}

// FindTarball locates the version whose dist.tarball has the given filename
// and returns that version's upstream tarball URL, integrity and shasum. The
// version named by the conventional filename is tried first.
func FindTarball(doc []byte, pkg, file string) (Dist, bool, error) {
	top, err := parsePackument(doc)
	if err != nil {
		return Dist{}, false, err
	}

	versions, err := objectOf(top["versions"])
	if err != nil {
		return Dist{}, false, err
	}

	// <unscoped>-<version>.tgz: try that version before scanning.
	if guess, ok := strings.CutSuffix(file, ".tgz"); ok {
		unscoped := pkg[strings.LastIndexByte(pkg, '/')+1:]
		if ver, ok := strings.CutPrefix(guess, unscoped+"-"); ok {
			if d, found, err := distOf(versions[ver], file); err != nil || found {
				return d, found, err
			}
		}
	}

	for _, raw := range versions {
		if d, found, err := distOf(raw, file); err != nil || found {
			return d, found, err
		}
	}

	return Dist{}, false, nil
}

// Dist is the dist object of one version.
type Dist struct {
	Tarball   string `json:"tarball"`
	Integrity string `json:"integrity"`
	Shasum    string `json:"shasum"`
}

func distOf(raw json.RawMessage, file string) (Dist, bool, error) {
	if len(raw) == 0 {
		return Dist{}, false, nil
	}

	var v struct {
		Dist Dist `json:"dist"`
	}

	if err := json.Unmarshal(raw, &v); err != nil {
		return Dist{}, false, fmt.Errorf("parse version: %w", err)
	}

	if TarballFileOf(v.Dist.Tarball) != file {
		return Dist{}, false, nil
	}

	return v.Dist, true, nil
}

// VersionManifest extracts one version's manifest from a packument; versionOrTag
// is an exact version or a dist-tag.
func VersionManifest(doc []byte, versionOrTag string) ([]byte, bool, error) {
	top, err := parsePackument(doc)
	if err != nil {
		return nil, false, err
	}

	versions, err := objectOf(top["versions"])
	if err != nil {
		return nil, false, err
	}

	if raw, ok := versions[versionOrTag]; ok {
		return raw, true, nil
	}

	tags, err := objectOf(top["dist-tags"])
	if err != nil {
		return nil, false, err
	}

	var ver string
	if err := json.Unmarshal(tags[versionOrTag], &ver); err != nil {
		return nil, false, nil //nolint:nilerr // absent or non-string tag: not found
	}

	raw, ok := versions[ver]

	return raw, ok, nil
}

// MergePackuments merges packuments of the same package in member priority
// order: versions are the union (the first member that has a version wins),
// each dist-tag comes from the first member that has it, time entries and every
// other top-level field likewise come from the first member that has them. A
// single document is returned unchanged.
func MergePackuments(docs [][]byte) ([]byte, error) {
	if len(docs) == 1 {
		return docs[0], nil
	}

	merged := make(map[string]json.RawMessage)
	unions := map[string]map[string]json.RawMessage{"versions": {}, "dist-tags": {}, "time": {}}

	var firstErr error

	parsedAny := false

	for _, doc := range docs {
		top, err := parsePackument(doc)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}

			continue
		}

		parsedAny = true

		for key, raw := range top {
			union, isUnion := unions[key]
			if !isUnion {
				if _, ok := merged[key]; !ok {
					merged[key] = raw
				}

				continue
			}

			obj, err := objectOf(raw)
			if err != nil {
				return nil, err
			}

			for k, v := range obj {
				if _, ok := union[k]; !ok {
					union[k] = v
				}
			}
		}
	}

	if !parsedAny {
		return nil, firstErr
	}

	for key, union := range unions {
		if len(union) == 0 {
			if key == "versions" || key == "dist-tags" {
				merged[key] = json.RawMessage(`{}`)
			}

			continue
		}

		raw, err := marshalObject(union)
		if err != nil {
			return nil, err
		}

		merged[key] = raw
	}

	return marshalPackument(merged)
}

// Integrity verification.

// ErrIntegrityMismatch reports tarball bytes that do not match the digest the
// packument advertises.
var ErrIntegrityMismatch = errors.New("npm tarball integrity mismatch")

type sriEntry struct {
	algo   string
	digest []byte
}

// VerifyDist hashes r and compares it with the digest in dist: dist.integrity
// (strongest listed SRI algorithm, any of its digests may match) or, when
// that is absent, dist.shasum. verified is false when dist advertises neither,
// in which case nothing was compared. A mismatch is ErrIntegrityMismatch.
func VerifyDist(r io.Reader, d Dist) (bool, error) {
	if entries := parseSRI(d.Integrity); len(entries) > 0 {
		return verifySRI(r, entries)
	}

	if d.Shasum == "" {
		return false, nil
	}

	want, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(d.Shasum)))
	if err != nil {
		return false, fmt.Errorf("%w: malformed shasum", ErrIntegrityMismatch)
	}

	h := sha1.New() //nolint:gosec // dist.shasum is SHA-1 by protocol
	if _, err := io.Copy(h, r); err != nil {
		return false, fmt.Errorf("hash tarball: %w", err)
	}

	if subtle.ConstantTimeCompare(h.Sum(nil), want) != 1 {
		return false, ErrIntegrityMismatch
	}

	return true, nil
}

// sriStrength orders the SRI algorithms npm understands.
var sriStrength = map[string]int{"sha1": 1, "sha256": 2, "sha384": 3, "sha512": 4} //nolint:gochecknoglobals // immutable

func parseSRI(integrity string) []sriEntry {
	var out []sriEntry

	for tok := range strings.FieldsSeq(integrity) {
		algo, rest, ok := strings.Cut(tok, "-")
		if !ok || sriStrength[algo] == 0 {
			continue
		}

		b64, _, _ := strings.Cut(rest, "?")

		digest, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(digest) == 0 {
			continue
		}

		out = append(out, sriEntry{algo: algo, digest: digest})
	}

	return out
}

func newSRIHash(algo string) hash.Hash {
	switch algo {
	case "sha1":
		return sha1.New() //nolint:gosec // SRI sha1 is accepted for legacy packages
	case "sha256":
		return sha256.New()
	case "sha384":
		return sha512.New384()
	default:
		return sha512.New()
	}
}

func verifySRI(r io.Reader, entries []sriEntry) (bool, error) {
	best := entries[0].algo
	for _, e := range entries[1:] {
		if sriStrength[e.algo] > sriStrength[best] {
			best = e.algo
		}
	}

	h := newSRIHash(best)
	if _, err := io.Copy(h, r); err != nil {
		return false, fmt.Errorf("hash tarball: %w", err)
	}

	sum := h.Sum(nil)

	for _, e := range entries {
		if e.algo == best && subtle.ConstantTimeCompare(sum, e.digest) == 1 {
			return true, nil
		}
	}

	return false, ErrIntegrityMismatch
}
