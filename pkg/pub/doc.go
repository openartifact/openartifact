// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"time"
)

const (
	// MaxDocBytes bounds a package document read from an upstream. Packages with
	// hundreds of releases carry a full pubspec each, so it is generous.
	MaxDocBytes = 64 << 20
	// MaxVersions bounds the releases of one package document.
	MaxVersions = 50_000
	// ContentType is the media type of the package documents and of the Accept
	// header the pub client sends for them.
	ContentType = "application/vnd.pub.v2+json"
)

// ErrInvalidDoc marks a document that is not a package document of the spec.
var ErrInvalidDoc = errors.New("invalid pub package document")

// Version is one release of a package document. Fields the spec does not name
// (and the ones a repository adds) are kept as they came, so a proxy hands a
// client everything the upstream said except the archive location.
type Version struct {
	Version    string
	ArchiveURL string
	// SHA256 is the lower-case hex digest of the archive, "" when none is listed.
	SHA256 string
	// Published is when the release was published, zero when the document has no time.
	Published time.Time
	// Retracted marks a release its publisher withdrew.
	Retracted bool

	fields map[string]json.RawMessage
}

// NewVersion builds the document entry of a release a repository holds.
// pubspec is the release's pubspec as JSON.
func NewVersion(version, archiveURL, sha256 string, published time.Time, pubspec json.RawMessage) *Version {
	v := &Version{
		Version: version, ArchiveURL: archiveURL, SHA256: sha256, Published: published,
		fields: map[string]json.RawMessage{},
	}

	if len(pubspec) == 0 {
		pubspec = json.RawMessage(`{}`)
	}

	v.fields["pubspec"] = pubspec

	return v
}

// Pubspec is the pubspec of the release as JSON, nil when the entry has none.
func (v *Version) Pubspec() json.RawMessage { return v.fields["pubspec"] }

// Doc is the package document of the spec: GET /api/packages/<name>.
type Doc struct {
	Name     string
	Versions []*Version

	// extra holds the top-level fields besides name, latest and versions
	// (isDiscontinued, replacedBy, advisoriesUpdated, ...).
	extra map[string]json.RawMessage
}

// NewDoc builds the document of a package from the releases a repository holds.
func NewDoc(name string, versions []*Version) *Doc {
	return &Doc{Name: name, Versions: versions}
}

// ParseDoc reads a package document. Entries without a valid version or without
// an archive URL are dropped, and the first entry of a repeated version wins.
func ParseDoc(data []byte) (*Doc, error) {
	if len(data) > MaxDocBytes {
		return nil, fmt.Errorf("%w: larger than %d bytes", ErrInvalidDoc, MaxDocBytes)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidDoc, err)
	}

	d := &Doc{}
	if err := json.Unmarshal(top["name"], &d.Name); err != nil || !ValidName(d.Name) {
		return nil, fmt.Errorf("%w: no valid package name", ErrInvalidDoc)
	}

	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(top["versions"], &entries); err != nil {
		return nil, fmt.Errorf("%w: no versions list", ErrInvalidDoc)
	}

	if len(entries) > MaxVersions {
		return nil, fmt.Errorf("%w: more than %d versions", ErrInvalidDoc, MaxVersions)
	}

	delete(top, "name")
	delete(top, "versions")
	delete(top, "latest")

	if len(top) > 0 {
		d.extra = top
	}

	seen := make(map[string]struct{}, len(entries))

	for _, fields := range entries {
		v := parseVersion(fields)
		if v == nil {
			continue
		}

		if _, dup := seen[v.Version]; dup {
			continue
		}

		seen[v.Version] = struct{}{}

		d.Versions = append(d.Versions, v)
	}

	return d, nil
}

// parseVersion reads one entry of a versions list, nil when it is unusable.
func parseVersion(fields map[string]json.RawMessage) *Version {
	v := &Version{fields: fields}

	if json.Unmarshal(fields["version"], &v.Version) != nil || !ValidVersion(v.Version) {
		return nil
	}

	if json.Unmarshal(fields["archive_url"], &v.ArchiveURL) != nil || v.ArchiveURL == "" {
		return nil
	}

	if u, err := url.Parse(v.ArchiveURL); err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
		return nil
	}

	var sum string
	if json.Unmarshal(fields["archive_sha256"], &sum) == nil && validSHA256(sum) {
		v.SHA256 = sum
	}

	var published string
	if json.Unmarshal(fields["published"], &published) == nil {
		v.Published, _ = time.Parse(time.RFC3339Nano, published)
	}

	_ = json.Unmarshal(fields["retracted"], &v.Retracted)

	return v
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}

	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// Find returns the entry of version, nil when the document has none.
func (d *Doc) Find(version string) *Version {
	for _, v := range d.Versions {
		if v.Version == version {
			return v
		}
	}

	return nil
}

// Link maps a release to the archive URL a client is told to fetch it from.
type Link func(v *Version) string

// MarshalVersion renders one release entry with its archive URL replaced by
// link(v).
func MarshalVersion(v *Version, link Link) (json.RawMessage, error) {
	fields := make(map[string]json.RawMessage, len(v.fields)+5)
	maps.Copy(fields, v.fields)

	set := func(key string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}

		fields[key] = raw

		return nil
	}

	if err := set("version", v.Version); err != nil {
		return nil, err
	}

	if err := set("archive_url", link(v)); err != nil {
		return nil, err
	}

	if v.SHA256 != "" {
		if err := set("archive_sha256", v.SHA256); err != nil {
			return nil, err
		}
	}

	if !v.Published.IsZero() {
		if err := set("published", v.Published.UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
	}

	if v.Retracted {
		if err := set("retracted", true); err != nil {
			return nil, err
		}
	}

	return json.Marshal(fields)
}

// Latest returns the release a client resolves "any" to: the highest stable
// release that is not retracted, else the highest pre-release that is not
// retracted, else the highest release. nil for an empty list.
func Latest(versions []*Version) *Version {
	var stable, pre, highest *Version

	for _, v := range versions {
		if highest == nil || CompareVersions(v.Version, highest.Version) > 0 {
			highest = v
		}

		if v.Retracted {
			continue
		}

		if IsPrerelease(v.Version) {
			if pre == nil || CompareVersions(v.Version, pre.Version) > 0 {
				pre = v
			}

			continue
		}

		if stable == nil || CompareVersions(v.Version, stable.Version) > 0 {
			stable = v
		}
	}

	switch {
	case stable != nil:
		return stable
	case pre != nil:
		return pre
	}

	return highest
}

// Marshal renders the document with every archive URL replaced by link(v),
// listing only the releases keep accepts (nil keeps all), and recomputes
// "latest" from what is listed.
func (d *Doc) Marshal(link Link, keep func(*Version) bool) ([]byte, error) {
	listed := make([]*Version, 0, len(d.Versions))

	for _, v := range d.Versions {
		if keep == nil || keep(v) {
			listed = append(listed, v)
		}
	}

	top := make(map[string]json.RawMessage, len(d.extra)+3)
	maps.Copy(top, d.extra)

	name, err := json.Marshal(d.Name)
	if err != nil {
		return nil, err
	}

	top["name"] = name

	entries := make([]json.RawMessage, 0, len(listed))

	for _, v := range listed {
		raw, err := MarshalVersion(v, link)
		if err != nil {
			return nil, err
		}

		entries = append(entries, raw)
	}

	if top["versions"], err = json.Marshal(entries); err != nil {
		return nil, err
	}

	if latest := Latest(listed); latest != nil {
		i := slices.Index(listed, latest)
		top["latest"] = entries[i]
	}

	return json.Marshal(top)
}

// Merge combines the documents of one package from several repositories. A
// version listed by more than one document is taken from the first; the
// top-level fields besides the releases come from the first document; the
// releases are ordered by precedence.
func Merge(docs []*Doc) *Doc {
	if len(docs) == 0 {
		return nil
	}

	out := &Doc{Name: docs[0].Name, extra: docs[0].extra}
	seen := make(map[string]struct{})

	for _, d := range docs {
		for _, v := range d.Versions {
			if _, dup := seen[v.Version]; dup {
				continue
			}

			seen[v.Version] = struct{}{}

			out.Versions = append(out.Versions, v)
		}
	}

	slices.SortStableFunc(out.Versions, func(a, b *Version) int { return CompareVersions(a.Version, b.Version) })

	return out
}

// CheckDoc reads a package document from r and checks it is the one of name.
func CheckDoc(r io.Reader, name string) error {
	data, err := io.ReadAll(io.LimitReader(r, MaxDocBytes+1))
	if err != nil {
		return err
	}

	d, err := ParseDoc(data)
	if err != nil {
		return err
	}

	if d.Name != name {
		return fmt.Errorf("%w: document of %q, wanted %q", ErrInvalidDoc, d.Name, name)
	}

	return nil
}
