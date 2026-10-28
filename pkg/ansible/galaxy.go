// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultLimit is the page size of a list without a limit parameter.
	DefaultLimit = 100
	// MaxLimit is the largest page a client can ask for.
	MaxLimit = 1000
)

// TimeFormat is the timestamp layout of the Galaxy API (UTC, microseconds).
const TimeFormat = "2006-01-02T15:04:05.000000Z"

// Stamp formats t the way the Galaxy API does.
func Stamp(t time.Time) string { return t.UTC().Format(TimeFormat) }

// Links builds the absolute URLs of one repository's Galaxy API v3. Every link
// the API hands out is absolute: ansible-core 2.15 cannot resolve a
// root-relative download_url.
type Links struct {
	// Root is the URL of the v3 API without a trailing slash, for example
	// https://host/repos/galaxy/api/v3.
	Root string
}

// Collections is the collection index.
func (l Links) Collections() string { return l.Root + "/collections/" }

// Collection is the document of one collection.
func (l Links) Collection(namespace, name string) string {
	return l.Collections() + namespace + "/" + name + "/"
}

// Versions is the version list of one collection.
func (l Links) Versions(namespace, name string) string {
	return l.Collection(namespace, name) + "versions/"
}

// Version is the detail document of one version.
func (l Links) Version(namespace, name, version string) string {
	return l.Versions(namespace, name) + url.PathEscape(version) + "/"
}

// Artifacts is the root of the artifact downloads.
func (l Links) Artifacts() string { return l.Root + "/artifacts/collections/" }

// Download is the URL of a collection tarball.
func (l Links) Download(filename string) string { return l.Artifacts() + url.PathEscape(filename) }

// Import is the URL of an import task.
func (l Links) Import(id string) string { return l.Root + "/imports/collections/" + id + "/" }

// Page is the pagination envelope of the Galaxy API v3.
type Page struct {
	Meta  map[string]int `json:"meta"`
	Links map[string]any `json:"links"`
	Data  any            `json:"data"`
}

// NewPage describes the window [offset, offset+limit) of count items of the
// list at listURL (without a query). The caller fills Data with the window.
func NewPage(listURL string, count, limit, offset int) Page {
	link := func(off int) string {
		return listURL + "?limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(off)
	}

	links := map[string]any{"first": link(0), "previous": nil, "next": nil, "last": link(max(count-1, 0) / limit * limit)}

	if offset > 0 {
		links["previous"] = link(max(offset-limit, 0))
	}

	if offset+limit < count {
		links["next"] = link(offset + limit)
	}

	return Page{Meta: map[string]int{"count": count}, Links: links}
}

// ErrPagination reports a limit or offset that is not a number in range.
var ErrPagination = errors.New("invalid pagination")

// PageParams reads limit and offset from q: a missing limit is DefaultLimit, a
// larger one is clamped to MaxLimit, and a value that is not a number (limit at
// least 1, offset at least 0) is ErrPagination.
func PageParams(q url.Values) (int, int, error) {
	limit, offset := DefaultLimit, 0

	for _, p := range []struct {
		key string
		dst *int
		min int
	}{{"limit", &limit, 1}, {"offset", &offset, 0}} {
		raw := q.Get(p.key)
		if raw == "" {
			continue
		}

		n, err := strconv.Atoi(raw)
		if err != nil || n < p.min {
			return 0, 0, fmt.Errorf("%w: %s must be a number of at least %d", ErrPagination, p.key, p.min)
		}

		*p.dst = n
	}

	return min(limit, MaxLimit), offset, nil
}

// Window returns items[offset:offset+limit] clamped to the slice.
func Window[T any](items []T, limit, offset int) []T {
	if offset >= len(items) {
		return nil
	}

	return items[offset:min(offset+limit, len(items))]
}

// VersionRef is an entry of a version list.
type VersionRef struct {
	Version         string   `json:"version"`
	Href            string   `json:"href"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	RequiresAnsible string   `json:"requires_ansible,omitempty"`
	Marks           []string `json:"marks"`
}

// Relink points the entry at version of namespace.name in l.
func (v *VersionRef) Relink(l Links, namespace, name string) {
	v.Href = l.Version(namespace, name, v.Version)
	if v.Marks == nil {
		v.Marks = []string{}
	}
}

// NewestFirst orders refs by descending version precedence.
func NewestFirst(refs []VersionRef) {
	slices.SortStableFunc(refs, func(a, b VersionRef) int { return CompareVersions(b.Version, a.Version) })
}

// Highest picks the highest release of refs (ordered newest first), or the
// highest pre-release when there is no release. refs must not be empty.
func Highest(refs []VersionRef) VersionRef {
	for _, v := range refs {
		if !IsPrerelease(v.Version) {
			return v
		}
	}

	return refs[0]
}

// VersionTag names a version inside a collection document.
type VersionTag struct {
	Href    string `json:"href"`
	Version string `json:"version"`
}

// CollectionDoc is the Galaxy document of a collection.
type CollectionDoc struct {
	Href           string      `json:"href"`
	Namespace      string      `json:"namespace"`
	Name           string      `json:"name"`
	Deprecated     bool        `json:"deprecated"`
	VersionsURL    string      `json:"versions_url"`
	HighestVersion *VersionTag `json:"highest_version,omitempty"`
	CreatedAt      string      `json:"created_at"`
	UpdatedAt      string      `json:"updated_at"`
}

// NewCollectionDoc builds the document of namespace.name in l from its version
// refs, newest first (at least one). The timestamps span all versions.
func NewCollectionDoc(l Links, namespace, name string, refs []VersionRef) CollectionDoc {
	top := Highest(refs)
	created, updated := refs[0].CreatedAt, refs[0].UpdatedAt

	for _, v := range refs {
		// Stamps share one fixed-width layout, so they order as strings.
		if v.CreatedAt != "" && (created == "" || v.CreatedAt < created) {
			created = v.CreatedAt
		}

		if v.UpdatedAt > updated {
			updated = v.UpdatedAt
		}
	}

	return CollectionDoc{
		Href:           l.Collection(namespace, name),
		Namespace:      namespace,
		Name:           name,
		VersionsURL:    l.Versions(namespace, name),
		HighestVersion: &VersionTag{Href: l.Version(namespace, name, top.Version), Version: top.Version},
		CreatedAt:      created,
		UpdatedAt:      updated,
	}
}

// ArtifactDoc describes the tarball of a version.
type ArtifactDoc struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// CollectionRefDoc is the collection a version belongs to.
type CollectionRefDoc struct {
	Name string `json:"name"`
	Href string `json:"href"`
}

// NamespaceDoc is the namespace of a version.
type NamespaceDoc struct {
	Name string `json:"name"`
}

// VersionDoc is the Galaxy document of one collection version.
type VersionDoc struct {
	Version         string            `json:"version"`
	Href            string            `json:"href"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
	RequiresAnsible string            `json:"requires_ansible,omitempty"`
	Marks           []string          `json:"marks"`
	Artifact        ArtifactDoc       `json:"artifact"`
	Collection      CollectionRefDoc  `json:"collection"`
	DownloadURL     string            `json:"download_url"`
	Name            string            `json:"name"`
	Namespace       NamespaceDoc      `json:"namespace"`
	Metadata        map[string]any    `json:"metadata"`
	Signatures      []json.RawMessage `json:"signatures"`
}

// Relink points every link of the document at l: its own href, its collection
// and the download of its artifact. The rest, including the artifact digest and
// any signatures, is kept as it is.
func (d *VersionDoc) Relink(l Links) {
	d.Href = l.Version(d.Namespace.Name, d.Name, d.Version)
	d.Collection.Href = l.Collection(d.Namespace.Name, d.Name)
	d.DownloadURL = l.Download(d.Artifact.Filename)

	if d.Marks == nil {
		d.Marks = []string{}
	}

	if d.Signatures == nil {
		d.Signatures = []json.RawMessage{}
	}

	if d.Metadata == nil {
		d.Metadata = map[string]any{}
	}
}

// Dependencies returns the dependency map of the document's metadata.
func (d *VersionDoc) Dependencies() map[string]string {
	raw, ok := d.Metadata["dependencies"].(map[string]any)
	if !ok {
		return nil
	}

	deps := make(map[string]string, len(raw))

	for k, v := range raw {
		if s, ok := v.(string); ok {
			deps[k] = s
		}
	}

	return deps
}

// MaxDocBytes bounds the size of a version list page or version detail
// document read from another server.
const MaxDocBytes = 8 << 20

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrDoc reports a Galaxy document that is not well formed.
var ErrDoc = errors.New("invalid galaxy document")

// ParseVersionDoc reads the detail document of namespace.name at version, as a
// Galaxy server answers it. The document must describe exactly that version and
// carry the digest and file name of its artifact: the file name must be
// NAMESPACE-NAME-VERSION.tar.gz, so a download URL built from it cannot leave
// the artifact directory. Dependencies, when present, must be well formed.
func ParseVersionDoc(r io.Reader, namespace, name, version string) (*VersionDoc, error) {
	var d VersionDoc

	dec := json.NewDecoder(io.LimitReader(r, MaxDocBytes))
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDoc, err)
	}

	d.Artifact.SHA256 = strings.ToLower(d.Artifact.SHA256)

	if d.Version != version || d.Name != name || d.Namespace.Name != namespace {
		return nil, fmt.Errorf("%w: document is for %s.%s %s", ErrDoc, d.Namespace.Name, d.Name, d.Version)
	}

	if d.Artifact.Filename != FileName(namespace, name, version) {
		return nil, fmt.Errorf("%w: unexpected artifact file name %q", ErrDoc, d.Artifact.Filename)
	}

	if !sha256Hex.MatchString(d.Artifact.SHA256) {
		return nil, fmt.Errorf("%w: artifact sha256 is not a digest", ErrDoc)
	}

	for dep, req := range d.Dependencies() {
		// Galaxy servers accept an empty requirement and read it as "*".
		if _, _, ok := SplitFQCN(dep); !ok || (req != "" && !ValidRequirement(req)) {
			return nil, fmt.Errorf("%w: invalid dependency %q", ErrDoc, dep)
		}
	}

	return &d, nil
}

// VersionPage is one page of a version list as a Galaxy server answers it.
type VersionPage struct {
	// Count is the number of versions of the whole list.
	Count int
	// Next is the link to the next page, empty on the last one.
	Next string
	// Versions are the entries of this page.
	Versions []VersionRef
}

// ParseVersionPage reads one page of the version list of a collection. An
// entry that is not a valid version is an error.
func ParseVersionPage(r io.Reader) (*VersionPage, error) {
	var doc struct {
		Meta struct {
			Count int `json:"count"`
		} `json:"meta"`
		Links struct {
			Next *string `json:"next"`
		} `json:"links"`
		Data []VersionRef `json:"data"`
	}

	if err := json.NewDecoder(io.LimitReader(r, MaxDocBytes)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDoc, err)
	}

	for _, v := range doc.Data {
		if !ValidVersion(v.Version) {
			return nil, fmt.Errorf("%w: invalid version %q", ErrDoc, v.Version)
		}
	}

	page := &VersionPage{Count: doc.Meta.Count, Versions: doc.Data}
	if doc.Links.Next != nil {
		page.Next = *doc.Links.Next
	}

	return page, nil
}
