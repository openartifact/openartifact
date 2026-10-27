// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ErrBadDocument is returned for a JSON document that is not what the Conan
// REST API sends.
var ErrBadDocument = errors.New("invalid conan document")

// ManifestFile is the file whose presence publishes a revision.
const ManifestFile = "conanmanifest.txt"

// timeLayouts are the ISO 8601 forms servers write revision times in:
// "2025-12-09T12:51:39.337+0000" (Artifactory) and
// "2025-12-09T12:51:39.337000+00:00" (conan_server, this server).
var timeLayouts = []string{"2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05Z07:00"} //nolint:gochecknoglobals // read-only table

// Revision is a published recipe or package revision and when it was published.
// Time keeps the server's own spelling, which the client parses itself.
type Revision struct {
	ID   string `json:"revision"`
	Time string `json:"time"`
}

// At is the publication time of r.
func (r Revision) At() time.Time {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, r.Time); err == nil {
			return t
		}
	}

	return time.Time{}
}

// valid reports whether r names a revision and carries a time that can be
// ordered.
func (r Revision) valid() bool {
	return ValidRevision(r.ID) && r.ID != "" && !r.At().IsZero()
}

// ParseRevision reads the answer of a latest endpoint.
func ParseRevision(doc []byte) (Revision, error) {
	var rev Revision
	if err := decode(doc, &rev); err != nil {
		return Revision{}, err
	}

	if !rev.valid() {
		return Revision{}, fmt.Errorf("%w: revision %q", ErrBadDocument, rev.ID)
	}

	return rev, nil
}

// ParseRevisions reads the answer of a revisions endpoint.
func ParseRevisions(doc []byte) ([]Revision, error) {
	var body struct {
		Revisions []Revision `json:"revisions"`
	}

	if err := decode(doc, &body); err != nil {
		return nil, err
	}

	for _, rev := range body.Revisions {
		if !rev.valid() {
			return nil, fmt.Errorf("%w: revision %q", ErrBadDocument, rev.ID)
		}
	}

	return body.Revisions, nil
}

// MergeRevisions unites revision lists: a revision listed by several keeps the
// entry of the first list that has it, and the result is newest first (equal
// times keep their order of appearance).
func MergeRevisions(lists ...[]Revision) []Revision {
	var out []Revision

	seen := make(map[string]struct{})

	for _, list := range lists {
		for _, rev := range list {
			if _, dup := seen[rev.ID]; !dup {
				seen[rev.ID] = struct{}{}
				out = append(out, rev)
			}
		}
	}

	slices.SortStableFunc(out, func(a, b Revision) int { return b.At().Compare(a.At()) })

	return out
}

// RenderRevision writes the answer of a latest endpoint.
func RenderRevision(rev Revision) []byte { return marshal(rev) }

// RenderRevisions writes the answer of a revisions endpoint.
func RenderRevisions(revs []Revision) []byte {
	if revs == nil {
		revs = []Revision{}
	}

	return marshal(map[string][]Revision{"revisions": revs})
}

// ParseFiles reads the answer of a file list endpoint: the file names, and
// whether the revision is published (its manifest is listed).
func ParseFiles(doc []byte) ([]string, bool, error) {
	var body struct {
		Files map[string]json.RawMessage `json:"files"`
	}

	if err := decode(doc, &body); err != nil {
		return nil, false, err
	}

	if body.Files == nil {
		return nil, false, fmt.Errorf("%w: no files", ErrBadDocument)
	}

	names := make([]string, 0, len(body.Files))

	for name := range body.Files {
		if !ValidFilename(name) {
			return nil, false, fmt.Errorf("%w: file name %q", ErrBadDocument, name)
		}

		names = append(names, name)
	}

	slices.Sort(names)

	return names, slices.Contains(names, ManifestFile), nil
}

// ParseSearch reads the answer of the recipe search: references written as
// name/version, with @user/channel when they have them.
func ParseSearch(doc []byte) ([]string, error) {
	var body struct {
		Results []string `json:"results"`
	}

	if err := decode(doc, &body); err != nil {
		return nil, err
	}

	for _, ref := range body.Results {
		if RefName(ref) == "" {
			return nil, fmt.Errorf("%w: reference %q", ErrBadDocument, ref)
		}
	}

	return body.Results, nil
}

// RenderSearch writes the answer of the recipe search.
func RenderSearch(results []string) []byte {
	if results == nil {
		results = []string{}
	}

	return marshal(map[string][]string{"results": results})
}

// RefName is the package name of a reference as the search writes it
// (name/version@user/channel); "" when ref has no name/version form.
func RefName(ref string) string {
	name, rest, ok := strings.Cut(ref, "/")
	if !ok || rest == "" || !validRefPart(name) {
		return ""
	}

	return name
}

// ParsePackageSearch reads the answer of a package search: the object of each
// package id, kept as written (the client reads its settings and options).
func ParsePackageSearch(doc []byte) (map[string]json.RawMessage, error) {
	var body map[string]json.RawMessage

	if err := decode(doc, &body); err != nil {
		return nil, err
	}

	for pid, info := range body {
		if !ValidRevision(pid) || pid == "" || !bytes.HasPrefix(bytes.TrimSpace(info), []byte("{")) {
			return nil, fmt.Errorf("%w: package %q", ErrBadDocument, pid)
		}
	}

	return body, nil
}

// MergePackageSearch unites package searches: a package id found by several
// keeps the entry of the first.
func MergePackageSearch(docs ...map[string]json.RawMessage) []byte {
	out := map[string]json.RawMessage{}

	for _, doc := range docs {
		for pid, info := range doc {
			if _, dup := out[pid]; !dup {
				out[pid] = info
			}
		}
	}

	return marshal(out)
}

// decode unmarshals doc, a single JSON value, into v.
func decode(doc []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrBadDocument, err)
	}

	if dec.More() {
		return fmt.Errorf("%w: trailing data", ErrBadDocument)
	}

	return nil
}

// marshal encodes v, a map or struct of strings and of raw documents that were
// decoded from valid JSON, which cannot fail to encode.
func marshal(v any) []byte {
	out, _ := json.Marshal(v) //nolint:errchkjson // strings and decoded raw messages always marshal

	return out
}

// ParseFileMap reads a Conan 1 document that maps file names to a string: the
// snapshot (file to MD5) or the download and digest URLs (file to URL).
func ParseFileMap(doc []byte) (map[string]string, error) {
	var m map[string]string
	if err := decode(doc, &m); err != nil {
		return nil, err
	}

	if m == nil {
		return nil, fmt.Errorf("%w: no files", ErrBadDocument)
	}

	for name, v := range m {
		if !ValidFilename(name) || v == "" {
			return nil, fmt.Errorf("%w: file %q", ErrBadDocument, name)
		}
	}

	return m, nil
}

// RenderFileMap writes a document ParseFileMap reads.
func RenderFileMap(m map[string]string) []byte { return marshal(m) }
