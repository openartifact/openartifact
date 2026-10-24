// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"
)

// RegistrationPageSize is the number of versions per page of a merged
// registration index (the size the hosted handler uses).
const RegistrationPageSize = 64

var (
	// ErrNoResources is returned for a service index that lists neither a
	// package base address nor a registration base.
	ErrNoResources = errors.New("nuget service index lists no usable resources")

	// ErrDigestMismatch is returned when a package does not match its
	// published size or hash.
	ErrDigestMismatch = errors.New("nuget package does not match its published digest")

	// ErrBadPackage is returned for a .nupkg that is not a zip with a root nuspec.
	ErrBadPackage = errors.New("not a nuget package")

	// ErrNotObject is returned for a JSON document that is not an object.
	ErrNotObject = errors.New("nuget document is not a JSON object")
)

// Preference order of the service index resource types, best first. The
// registration types are ordered by capability: the unversioned
// "RegistrationsBaseUrl" (semver1 only) is the last resort.
//
//nolint:gochecknoglobals // constant preference tables
var (
	registrationTypes = []string{
		"RegistrationsBaseUrl/Versioned", "RegistrationsBaseUrl/3.6.0", "RegistrationsBaseUrl/3.4.0",
		"RegistrationsBaseUrl/3.0.0-rc", "RegistrationsBaseUrl/3.0.0-beta", "RegistrationsBaseUrl",
	}
	searchTypes = []string{
		"SearchQueryService/3.5.0", "SearchQueryService", "SearchQueryService/3.0.0-rc", "SearchQueryService/3.0.0-beta",
	}
	autocompleteTypes = []string{
		"SearchAutocompleteService/3.5.0", "SearchAutocompleteService", "SearchAutocompleteService/3.0.0-rc",
		"SearchAutocompleteService/3.0.0-beta",
	}
)

// ServiceIndex is the part of a NuGet v3 service index (index.json) a proxy
// serves: the flat container ("PackageBaseAddress/3.0.0"), the registrations
// and the search endpoints. Base URLs end in a slash.
type ServiceIndex struct {
	PackageBase       string
	Registrations     string
	RegistrationTypes []string
	Search            string
	SearchType        string
	Autocomplete      string
	AutocompleteType  string
}

// ParseServiceIndex picks the resources a proxy needs out of an upstream
// service index: the best registrations base, the first package base address
// and the first search and autocomplete endpoints of the best type. Resources
// whose URL is not absolute http(s) are ignored.
func ParseServiceIndex(doc []byte) (*ServiceIndex, error) {
	var in struct {
		Resources []struct {
			ID   string          `json:"@id"`
			Type json.RawMessage `json:"@type"`
		} `json:"resources"`
	}

	if err := json.Unmarshal(doc, &in); err != nil {
		return nil, fmt.Errorf("parse service index: %w", err)
	}

	byType := make(map[string]string, len(in.Resources))

	for _, res := range in.Resources {
		if !absoluteHTTP(res.ID) {
			continue
		}

		for _, t := range resourceTypes(res.Type) {
			if _, dup := byType[t]; !dup {
				byType[t] = res.ID
			}
		}
	}

	idx := &ServiceIndex{PackageBase: withSlash(byType["PackageBaseAddress/3.0.0"])}

	for _, t := range registrationTypes {
		if id, ok := byType[t]; ok {
			idx.Registrations, idx.RegistrationTypes = withSlash(id), []string{t}

			break
		}
	}

	idx.Search, idx.SearchType = pickType(byType, searchTypes)
	idx.Autocomplete, idx.AutocompleteType = pickType(byType, autocompleteTypes)

	if idx.PackageBase == "" && idx.Registrations == "" {
		return nil, ErrNoResources
	}

	return idx, nil
}

func pickType(byType map[string]string, prefs []string) (string, string) {
	for _, t := range prefs {
		if id, ok := byType[t]; ok {
			return id, t
		}
	}

	return "", ""
}

func resourceTypes(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}

	var many []string

	_ = json.Unmarshal(raw, &many) // anything else is no resource type

	return many
}

func absoluteHTTP(s string) bool {
	u, err := url.Parse(s)

	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func withSlash(s string) string {
	if s == "" || strings.HasSuffix(s, "/") {
		return s
	}

	return s + "/"
}

// Render returns the service index of a repository served at repoURL
// (".../repos/<name>"): the resources s has, pointing at the repository's
// own /v3/ routes.
func (s *ServiceIndex) Render(repoURL string) ([]byte, error) {
	var res []map[string]string

	add := func(id, typ string) {
		res = append(res, map[string]string{"@id": id, "@type": typ})
	}

	for _, t := range s.RegistrationTypes {
		add(repoURL+"/v3/registration/", t)
	}

	if s.PackageBase != "" {
		add(repoURL+"/v3/package/", "PackageBaseAddress/3.0.0")
	}

	if s.Search != "" {
		add(repoURL+"/v3/query", s.SearchType)
	}

	if s.Autocomplete != "" {
		add(repoURL+"/v3/autocomplete", s.AutocompleteType)
	}

	out, err := json.Marshal(map[string]any{"version": "3.0.0", "resources": res})
	if err != nil {
		return nil, fmt.Errorf("render service index: %w", err)
	}

	return out, nil
}

// URLPrefix replaces the URL prefix From with To.
type URLPrefix struct{ From, To string }

// RewriteURLs replaces, in every string value of the JSON document doc, the
// longest leading From of pairs by its To. Keys are left alone.
func RewriteURLs(doc []byte, pairs ...URLPrefix) ([]byte, error) {
	v, err := decodeJSON(doc)
	if err != nil {
		return nil, err
	}

	return encodeJSON(rewrite(v, pairs))
}

// RewriteRegistrationIndex is RewriteURLs for a registration index, which
// also points the pages the upstream did not inline at pagePrefix (the
// "<repo>/v3/registration/<id>/page/" URL of the serving repository): a page
// is fetched as <pagePrefix><lower>/<upper>.json whatever the upstream calls it.
func RewriteRegistrationIndex(doc []byte, pagePrefix string, pairs ...URLPrefix) ([]byte, error) {
	v, err := decodeJSON(doc)
	if err != nil {
		return nil, err
	}

	if top, ok := v.(map[string]any); ok {
		pages, _ := top["items"].([]any)
		for _, p := range pages {
			page, _ := p.(map[string]any)
			lower, _ := page["lower"].(string)
			upper, _ := page["upper"].(string)

			if _, inline := page["items"]; page != nil && !inline && lower != "" && upper != "" {
				page["@id"] = pagePrefix + url.PathEscape(lower) + "/" + url.PathEscape(upper) + ".json"
			}
		}
	}

	return encodeJSON(rewrite(v, pairs))
}

func rewrite(v any, pairs []URLPrefix) any {
	switch t := v.(type) {
	case string:
		return rewriteString(t, pairs)
	case map[string]any:
		for k, e := range t {
			t[k] = rewrite(e, pairs)
		}
	case []any:
		for i, e := range t {
			t[i] = rewrite(e, pairs)
		}
	}

	return v
}

// rewriteString replaces the leading From of s that is the longest by its To:
// when one base URL lies below another (GitHub Packages serves the flat
// container at <registrations>download/), a URL below the inner base is the
// inner one's, whatever the order of pairs.
func rewriteString(s string, pairs []URLPrefix) string {
	best := -1

	for i, p := range pairs {
		if p.From != "" && strings.HasPrefix(s, p.From) && (best < 0 || len(p.From) > len(pairs[best].From)) {
			best = i
		}
	}

	if best < 0 {
		return s
	}

	return pairs[best].To + s[len(pairs[best].From):]
}

func decodeJSON(doc []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	return v, nil
}

func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// IsObject reports whether doc is a JSON object.
func IsObject(doc []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil || top == nil {
		return ErrNotObject
	}

	return nil
}

// Page is one page of a registration index. Inline is false when the
// upstream left the items out and the page has to be fetched from ID.
type Page struct {
	ID, Lower, Upper string
	Inline           bool
	Items            []json.RawMessage
}

// ParsePages returns the pages of a registration index.
func ParsePages(doc []byte) ([]Page, error) {
	var in struct {
		Items []struct {
			ID    string            `json:"@id"`
			Lower string            `json:"lower"`
			Upper string            `json:"upper"`
			Items []json.RawMessage `json:"items"`
		} `json:"items"`
	}

	if err := json.Unmarshal(doc, &in); err != nil {
		return nil, fmt.Errorf("parse registration index: %w", err)
	}

	pages := make([]Page, len(in.Items))
	for i, p := range in.Items {
		pages[i] = Page{ID: p.ID, Lower: p.Lower, Upper: p.Upper, Inline: p.Items != nil, Items: p.Items}
	}

	return pages, nil
}

// ParsePageItems returns the package items of a registration page document.
func ParsePageItems(doc []byte) ([]json.RawMessage, error) {
	var in struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := json.Unmarshal(doc, &in); err != nil {
		return nil, fmt.Errorf("parse registration page: %w", err)
	}

	return in.Items, nil
}

// itemVersion is the normalized version a registration item describes: its
// catalog entry's version, else the "<version>.json" its @id names.
func itemVersion(item json.RawMessage) (string, bool) {
	var in struct {
		ID           string          `json:"@id"`
		CatalogEntry json.RawMessage `json:"catalogEntry"` //nolint:tagliatelle // NuGet wire format
	}

	if err := json.Unmarshal(item, &in); err != nil {
		return "", false
	}

	var entry struct {
		Version string `json:"version"`
	}

	if json.Unmarshal(in.CatalogEntry, &entry) == nil && entry.Version != "" {
		v, _ := Normalize(entry.Version)
		return v, true
	}

	if name, ok := strings.CutSuffix(path.Base(in.ID), ".json"); ok && name != "" && name != "index" {
		v, _ := Normalize(name)
		return v, true
	}

	return "", false
}

// MergeRegistrationItems builds the registration index served at indexURL
// from the items of several sources, in priority order: one item per
// version (the first source that has it wins), ordered by version and cut
// into inline pages.
func MergeRegistrationItems(indexURL string, sources ...[]json.RawMessage) ([]byte, error) {
	type entry struct {
		version string
		item    json.RawMessage
	}

	var (
		entries []entry
		seen    = map[string]bool{}
	)

	for _, items := range sources {
		for _, it := range items {
			v, ok := itemVersion(it)
			if !ok || seen[v] {
				continue
			}

			seen[v] = true

			entries = append(entries, entry{v, it})
		}
	}

	slices.SortStableFunc(entries, func(a, b entry) int { return CompareStrings(a.version, b.version) })

	pages := make([]map[string]any, 0, len(entries)/RegistrationPageSize+1)

	for chunk := range slices.Chunk(entries, RegistrationPageSize) {
		items := make([]json.RawMessage, len(chunk))
		for i, e := range chunk {
			items[i] = e.item
		}

		lower, upper := chunk[0].version, chunk[len(chunk)-1].version
		pages = append(pages, map[string]any{
			"@id":    indexURL + "#page/" + lower + "/" + upper,
			"@type":  "catalog:CatalogPage",
			"count":  len(items),
			"items":  items,
			"lower":  lower,
			"parent": indexURL,
			"upper":  upper,
		})
	}

	out, err := json.Marshal(map[string]any{"@id": indexURL, "count": len(pages), "items": pages})
	if err != nil {
		return nil, fmt.Errorf("render registration index: %w", err)
	}

	return out, nil
}

// MergeVersionLists unites flat-container version lists ({"versions":[...]}):
// one entry per version, ordered by version.
func MergeVersionLists(docs ...[]byte) ([]byte, error) {
	var (
		versions []string
		seen     = map[string]bool{}
	)

	for _, doc := range docs {
		var in struct {
			Versions []string `json:"versions"`
		}

		if err := json.Unmarshal(doc, &in); err != nil {
			return nil, fmt.Errorf("parse version list: %w", err)
		}

		for _, v := range in.Versions {
			n, _ := Normalize(v)
			if !seen[n] {
				seen[n] = true

				versions = append(versions, n)
			}
		}
	}

	slices.SortStableFunc(versions, CompareStrings)

	if versions == nil {
		versions = []string{}
	}

	out, err := json.Marshal(map[string]any{"versions": versions})
	if err != nil {
		return nil, fmt.Errorf("render version list: %w", err)
	}

	return out, nil
}

// CatalogEntryURL returns the catalog entry URL of a registration leaf
// document (its "catalogEntry" is a URL, or an object with an @id), "" when
// it has none.
func CatalogEntryURL(leaf []byte) string {
	var in struct {
		CatalogEntry json.RawMessage `json:"catalogEntry"` //nolint:tagliatelle // NuGet wire format
	}

	if json.Unmarshal(leaf, &in) != nil {
		return ""
	}

	var s string
	if json.Unmarshal(in.CatalogEntry, &s) == nil {
		return s
	}

	var obj struct {
		ID string `json:"@id"`
	}

	_ = json.Unmarshal(in.CatalogEntry, &obj) // not an object: no URL

	return obj.ID
}

// PageURL returns the upstream URL of the page spanning lower..upper in a
// registration index, with any fragment removed; ok is false for no such
// non-inline page.
func PageURL(pages []Page, lower, upper string) (string, bool) {
	for _, p := range pages {
		if p.Inline || p.Lower != lower || p.Upper != upper || p.ID == "" {
			continue
		}

		u, _, _ := strings.Cut(p.ID, "#")

		return u, true
	}

	return "", false
}

// Digest is what a package catalog entry publishes about a .nupkg. Hash is
// nil and Size 0 for what the entry does not say.
type Digest struct {
	Algorithm string // "SHA512" or "SHA256"
	Hash      []byte
	Size      int64
}

// Known reports whether d constrains a package at all.
func (d Digest) Known() bool { return len(d.Hash) > 0 || d.Size > 0 }

// ParseCatalogDigest reads packageHash/packageHashAlgorithm/packageSize from
// a catalog entry document. A hash of an algorithm other than SHA-512 or
// SHA-256, or one that is not base64, is ignored (the size still applies).
func ParseCatalogDigest(doc []byte) Digest {
	var in struct {
		Hash      string `json:"packageHash"`          //nolint:tagliatelle // NuGet wire format
		Algorithm string `json:"packageHashAlgorithm"` //nolint:tagliatelle // NuGet wire format
		Size      int64  `json:"packageSize"`          //nolint:tagliatelle // NuGet wire format
	}

	if json.Unmarshal(doc, &in) != nil {
		return Digest{}
	}

	d := Digest{Size: max(in.Size, 0)}

	algo := strings.ToUpper(in.Algorithm)
	if algo == "" {
		algo = "SHA512"
	}

	if algo != "SHA512" && algo != "SHA256" {
		return d
	}

	if h, err := base64.StdEncoding.DecodeString(in.Hash); err == nil && len(h) > 0 {
		d.Algorithm, d.Hash = algo, h
	}

	return d
}

// Verify checks the package read from r against d: its size and its hash,
// whichever d has. r is left at an unspecified offset.
func (d Digest) Verify(r io.ReadSeeker) error {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("measure package: %w", err)
	}

	if d.Size > 0 && size != d.Size {
		return fmt.Errorf("%w: size %d, published %d", ErrDigestMismatch, size, d.Size)
	}

	if len(d.Hash) == 0 {
		return nil
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind package: %w", err)
	}

	var h hash.Hash

	if d.Algorithm == "SHA256" {
		h = sha256.New()
	} else {
		h = sha512.New()
	}

	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("hash package: %w", err)
	}

	if subtle.ConstantTimeCompare(h.Sum(nil), d.Hash) != 1 {
		return fmt.Errorf("%w: %s differs", ErrDigestMismatch, d.Algorithm)
	}

	return nil
}

// ValidatePackage checks that r holds a zip archive with a nuspec at its
// root: the structure every .nupkg has, enough to catch a truncated or
// substituted body when the upstream publishes no digest.
func ValidatePackage(r io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadPackage, err)
	}

	for _, f := range zr.File {
		if !strings.Contains(f.Name, "/") && strings.HasSuffix(strings.ToLower(f.Name), ".nuspec") {
			return nil
		}
	}

	return fmt.Errorf("%w: no nuspec", ErrBadPackage)
}
