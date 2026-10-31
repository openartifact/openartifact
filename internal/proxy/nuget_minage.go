// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgnuget "github.com/openartifact/openartifact/pkg/nuget"
)

// The minimum-age gate of the NuGet proxy (see minage.go).
//
// The publish time of a version is the "published" of its registration entry
// (the catalogEntry of an item of a registration index or page, the leaf's own
// "published", else its catalog entry's). An unlisted package states the year
// 1900 instead of a time: that is no date, so such a version passes.
//
//   - A registration index drops, from its inline pages, the items that are too
//     new (a page left empty goes, the others keep their lower/upper bounds,
//     which stay valid bounds, and get their count corrected); a page the
//     upstream does not inline is gated when it is served. An index with
//     nothing left answers 404, as a package without versions does.
//   - A registration page drops its too-new items; a leaf of a too-new
//     version answers 404.
//   - A flat container version list carries no times: they come from the
//     package's registration (its index and every page, through the same
//     cache). A version the registration does not know yet is too new (the flat
//     container is updated before the registration), unless the feed offers only
//     the semver 1 registrations, which lack the semver 2 versions. A feed
//     without registrations cannot date its versions, which then pass.
//   - The files (.nupkg, .nuspec) of a too-new version answer 403.
//
// Search and autocomplete results are passed through as the upstream sends
// them: they carry no publish times, so they still name a too-new version,
// whose files are refused all the same.

// nugetNoDate is the last year the NuGet "published" of an unlisted package
// can have (1900-01-01): it states no time.
const nugetNoDate = 1900

// nugetDates is when the versions of one package were published, by normalized
// version; the zero time marks a version the registration lists without a time.
type nugetDates struct {
	published map[string]time.Time
	// complete is whether a version missing from published is a version the
	// registration has not seen yet (rather than one it cannot hold).
	complete bool
}

// nugetGate is the minimum-age gate of one request.
type nugetGate struct {
	repo *models.Repository
	now  time.Time
	// dates are the publish times of the package a version list belongs to; nil
	// when the feed cannot date its versions.
	dates *nugetDates
}

// stamp is the gate's part of the validator of a rendered document, "" when
// the gate is off.
func (g nugetGate) stamp() string { return gateStamp(g.repo, g.now) }

// parseNuGetPublished reads a "published" timestamp: the zero time when it is
// absent, unreadable or the year-1900 placeholder of an unlisted package.
func parseNuGetPublished(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= nugetNoDate {
		return time.Time{}
	}

	return t
}

// nugetItem is what the gate reads of a registration item or leaf.
type nugetItem struct {
	ID           string          `json:"@id"`
	Published    string          `json:"published"`
	CatalogEntry json.RawMessage `json:"catalogEntry"` //nolint:tagliatelle // NuGet wire format
}

// nugetEntry is the part of an inline catalog entry the gate reads.
type nugetEntry struct {
	Version   string `json:"version"`
	Published string `json:"published"`
}

// nugetItemMeta returns the normalized version ("" when the item does not
// name one) and the publish time (zero when unknown) of a registration item
// or leaf.
func nugetItemMeta(raw json.RawMessage) (string, time.Time) {
	var it nugetItem
	if json.Unmarshal(raw, &it) != nil {
		return "", time.Time{}
	}

	var entry nugetEntry

	published := parseNuGetPublished(it.Published)

	if json.Unmarshal(it.CatalogEntry, &entry) == nil {
		if t := parseNuGetPublished(entry.Published); !t.IsZero() {
			published = t
		}
	}

	version := entry.Version
	if version == "" {
		if name, ok := strings.CutSuffix(path.Base(it.ID), ".json"); ok && name != "index" {
			version = name
		}
	}

	if version != "" {
		version, _ = pkgnuget.Normalize(version)
	}

	return version, published
}

// gateNuGetItems returns the items of a registration page old enough at now.
func gateNuGetItems(repo *models.Repository, items []json.RawMessage, now time.Time) []json.RawMessage {
	kept := make([]json.RawMessage, 0, len(items))

	for _, it := range items {
		if _, published := nugetItemMeta(it); !tooNew(repo, published, now) {
			kept = append(kept, it)
		}
	}

	return kept
}

// gateNuGetRegIndex drops the too-new items of the inline pages of a
// registration index; errNotFound when no page is left.
func gateNuGetRegIndex(repo *models.Repository, doc []byte, now time.Time) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return nil, fmt.Errorf("%w: %w", errUnusableDoc, err)
	}

	var pages []json.RawMessage
	if err := json.Unmarshal(top["items"], &pages); err != nil || len(pages) == 0 {
		return doc, nil //nolint:nilerr // no pages: nothing to gate
	}

	var (
		out     = make([]json.RawMessage, 0, len(pages))
		changed bool
		inline  = true
	)

	for _, raw := range pages {
		page, state := gateNuGetInlinePage(repo, raw, now)

		switch state {
		case nugetPageKept:
			out = append(out, raw)
		case nugetPageNarrowed:
			changed = true

			out = append(out, page)
		case nugetPageEmptied:
			changed = true
		case nugetPageNotInline:
			// Gated when the page itself is served.
			inline = false

			out = append(out, raw)
		}
	}

	if !changed {
		return doc, nil
	}

	if len(out) == 0 && inline {
		return nil, errNotFound
	}

	var err error

	if top["items"], err = json.Marshal(out); err != nil {
		return nil, fmt.Errorf("render registration index: %w", err)
	}

	if top["count"], err = json.Marshal(len(out)); err != nil {
		return nil, fmt.Errorf("render registration index: %w", err)
	}

	return marshalNuGetObject(top)
}

// nugetPageState is what the gate made of one page of a registration index.
type nugetPageState int

const (
	nugetPageKept nugetPageState = iota
	nugetPageNarrowed
	nugetPageEmptied
	nugetPageNotInline
)

// gateNuGetInlinePage gates one page of a registration index; the returned page
// is the narrowed one for nugetPageNarrowed.
func gateNuGetInlinePage(repo *models.Repository, raw json.RawMessage, now time.Time) (json.RawMessage, nugetPageState) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields["items"] == nil {
		return nil, nugetPageNotInline
	}

	var items []json.RawMessage
	if json.Unmarshal(fields["items"], &items) != nil {
		return nil, nugetPageNotInline
	}

	left := gateNuGetItems(repo, items, now)

	switch {
	case len(left) == len(items):
		return raw, nugetPageKept
	case len(left) == 0:
		return nil, nugetPageEmptied
	}

	out, err := setNuGetItems(fields, left)
	if err != nil {
		return nil, nugetPageNotInline
	}

	return out, nugetPageNarrowed
}

// setNuGetItems renders a registration index page or page document with items
// as its items.
func setNuGetItems(fields map[string]json.RawMessage, items []json.RawMessage) ([]byte, error) {
	var err error

	if fields["items"], err = json.Marshal(items); err != nil {
		return nil, fmt.Errorf("render registration items: %w", err)
	}

	if fields["count"], err = json.Marshal(len(items)); err != nil {
		return nil, fmt.Errorf("render registration items: %w", err)
	}

	return marshalNuGetObject(fields)
}

func marshalNuGetObject(fields map[string]json.RawMessage) ([]byte, error) {
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("render nuget document: %w", err)
	}

	return out, nil
}

// gateNuGetRegPage drops the too-new items of a registration page document.
func gateNuGetRegPage(repo *models.Repository, doc []byte, now time.Time) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return nil, fmt.Errorf("%w: %w", errUnusableDoc, err)
	}

	var items []json.RawMessage
	if json.Unmarshal(fields["items"], &items) != nil {
		return doc, nil //nolint:nilerr // no items: nothing to gate
	}

	left := gateNuGetItems(repo, items, now)
	if len(left) == len(items) {
		return doc, nil
	}

	return setNuGetItems(fields, left)
}

// gateNuGetLeaf answers errNotFound for the leaf of a too-new version.
func gateNuGetLeaf(repo *models.Repository, doc []byte, now time.Time) error {
	if _, published := nugetItemMeta(doc); tooNew(repo, published, now) {
		return errNotFound
	}

	return nil
}

// gateNuGetVersions drops from a flat container version list the versions that
// are too new at now by dates; errNotFound when none is left. A nil dates
// leaves the list alone.
func gateNuGetVersions(repo *models.Repository, doc []byte, dates *nugetDates, now time.Time) ([]byte, error) {
	if dates == nil {
		return doc, nil
	}

	versions, err := pkgnuget.ParseVersionList(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errUnusableDoc, err)
	}

	kept := make([]string, 0, len(versions))

	for _, v := range versions {
		n, _ := pkgnuget.Normalize(v)

		published, known := dates.published[n]
		if (known && !tooNew(repo, published, now)) || (!known && !dates.complete) {
			kept = append(kept, v)
		}
	}

	switch {
	case len(kept) == len(versions):
		return doc, nil
	case len(kept) == 0:
		return nil, errNotFound
	}

	out, err := json.Marshal(map[string]any{"versions": kept})
	if err != nil {
		return nil, fmt.Errorf("render version list: %w", err)
	}

	return out, nil
}

// apply gates the upstream document doc (decompressed) of p.
func (g nugetGate) apply(doc []byte, p nugetPath) ([]byte, error) {
	if minAge(g.repo) == 0 {
		return doc, nil
	}

	switch p.kind {
	case nugetRegIndex:
		return gateNuGetRegIndex(g.repo, doc, g.now)
	case nugetRegPage:
		return gateNuGetRegPage(g.repo, doc, g.now)
	case nugetRegLeaf:
		return doc, gateNuGetLeaf(g.repo, doc, g.now)
	case nugetVersions:
		return gateNuGetVersions(g.repo, doc, g.dates, g.now)
	default:
		return doc, nil
	}
}

// nugetGateFor prepares the gate of a request for p: the publish times of the
// package's versions when p is its version list (read before the list is held,
// as they take documents of their own).
func (s *Service) nugetGateFor(
	ctx context.Context, repo *models.Repository, idx *pkgnuget.ServiceIndex, p nugetPath, now time.Time,
) (nugetGate, error) {
	g := nugetGate{repo: repo, now: now}

	if minAge(repo) == 0 || p.kind != nugetVersions {
		return g, nil
	}

	dates, err := s.nugetDatesOf(ctx, repo, idx, p.id)
	if err != nil {
		return nugetGate{}, err
	}

	g.dates = dates

	return g, nil
}

// nugetDatesOf collects the publish times of id's versions from its
// registration: the index and the pages it does not inline. nil when the feed
// has no registration of the package.
func (s *Service) nugetDatesOf(
	ctx context.Context, repo *models.Repository, idx *pkgnuget.ServiceIndex, id string,
) (*nugetDates, error) {
	if idx.Registrations == "" {
		return nil, nil //nolint:nilnil // no registrations: nothing to date the versions by
	}

	pages, _, err := parseDoc(ctx, s, repo, s.nugetDoc(repo, idx, nugetPath{kind: nugetRegIndex, id: id}),
		func(body []byte, _ *resolution) ([]pkgnuget.Page, error) {
			doc, gerr := gunzipDoc(body)
			if gerr != nil {
				return nil, gerr
			}

			return pkgnuget.ParsePages(doc) //nolint:wrapcheck // logged by parseDoc
		})
	if errors.Is(err, errNotFound) {
		return nil, nil //nolint:nilnil // the feed does not know the package
	}

	if err != nil {
		return nil, err
	}

	dates := &nugetDates{
		published: make(map[string]time.Time),
		complete:  len(idx.RegistrationTypes) > 0 && idx.RegistrationTypes[0] != "RegistrationsBaseUrl",
	}

	for _, page := range pages {
		items := page.Items
		if !page.Inline {
			if items, err = s.nugetPageItems(ctx, repo, idx, id, page); err != nil {
				return nil, err
			}
		}

		for _, it := range items {
			if v, published := nugetItemMeta(it); v != "" {
				dates.published[v] = published
			}
		}
	}

	return dates, nil
}

// nugetPageItems returns the items of a registration page the index does not
// inline, read through the cache. A page that cannot be requested from this
// repository (or that the upstream no longer has) has no items.
func (s *Service) nugetPageItems(
	ctx context.Context, repo *models.Repository, idx *pkgnuget.ServiceIndex, id string, page pkgnuget.Page,
) ([]json.RawMessage, error) {
	if !safeNuGetSegment(page.Lower) || !safeNuGetSegment(page.Upper) {
		return nil, nil
	}

	p := nugetPath{kind: nugetRegPage, id: id, lower: page.Lower, upper: page.Upper}

	items, _, err := parseDoc(ctx, s, repo, s.nugetDoc(repo, idx, p),
		func(body []byte, _ *resolution) ([]json.RawMessage, error) {
			doc, gerr := gunzipDoc(body)
			if gerr != nil {
				return nil, gerr
			}

			return pkgnuget.ParsePageItems(doc) //nolint:wrapcheck // logged by parseDoc
		})
	if errors.Is(err, errNotFound) {
		return nil, nil
	}

	return items, err
}

// nugetMeta is what the registration leaf and catalog entry of a package tell:
// the digest its .nupkg must match and when it was published.
type nugetMeta struct {
	digest    pkgnuget.Digest
	published time.Time
}

// nugetLeaf is what a registration leaf yields: where its catalog entry is and
// the leaf's own publish time.
type nugetLeaf struct {
	catalog   string
	published time.Time
}

// nugetLeafMeta looks up what the upstream publishes about a package file:
// the catalog entry its registration leaf names carries packageHash and
// packageSize, and the leaf (or that entry) the publish time. The catalog is
// only fetched when it is needed: for the digest of a .nupkg (wantDigest), or
// for the publish time the leaf does not state. An upstream without a leaf or
// catalog (or whose catalog entry has neither) yields the zero value; any other
// failure is returned, so a package is never accepted unverified or undated
// because the check could not run.
func (s *Service) nugetLeafMeta(
	ctx context.Context,
	repo *models.Repository,
	idx *pkgnuget.ServiceIndex,
	p nugetPath,
	wantDigest bool,
) (nugetMeta, error) {
	leafPath := nugetPath{kind: nugetRegLeaf, id: p.id, version: p.version}

	leaf, _, err := parseDoc(ctx, s, repo, s.nugetDoc(repo, idx, leafPath),
		func(body []byte, _ *resolution) (nugetLeaf, error) {
			doc, gerr := gunzipDoc(body)
			if gerr != nil {
				return nugetLeaf{}, gerr
			}

			_, published := nugetItemMeta(doc)

			return nugetLeaf{catalog: pkgnuget.CatalogEntryURL(doc), published: published}, nil
		})
	if errors.Is(err, errNotFound) {
		return nugetMeta{}, nil
	}

	if err != nil {
		return nugetMeta{}, err
	}

	meta := nugetMeta{published: leaf.published}

	needCatalog := wantDigest || (minAge(repo) > 0 && leaf.published.IsZero())
	if !needCatalog || leaf.catalog == "" || leaf.catalog == nugetDocURL(idx, leafPath) {
		return meta, nil
	}

	doc, err := s.fetchNuGetDocument(ctx, repo, leaf.catalog)
	if errors.Is(err, errNotFound) {
		return meta, nil
	}

	if err != nil {
		return nugetMeta{}, err
	}

	meta.digest = pkgnuget.ParseCatalogDigest(doc)

	if meta.published.IsZero() {
		meta.published = parseNuGetCatalogPublished(doc)
	}

	return meta, nil
}

// parseNuGetCatalogPublished reads the publish time of a catalog entry document.
func parseNuGetCatalogPublished(doc []byte) time.Time {
	var in struct {
		Published string `json:"published"`
	}

	if json.Unmarshal(doc, &in) != nil {
		return time.Time{}
	}

	return parseNuGetPublished(in.Published)
}
