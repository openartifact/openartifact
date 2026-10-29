// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
)

const (
	// ansibleMetaMaxAge is how long shared caches may reuse a public group's
	// generated Galaxy documents before revalidating.
	ansibleMetaMaxAge = 60

	// ansibleMaxPages bounds the pages read from one member for one list.
	ansibleMaxPages = 100

	// ansibleMaxEntries bounds the entries read from one member for one list.
	ansibleMaxEntries = 100000
)

// serveAnsible answers the Galaxy API requests of an ansible group that it
// generates or merges and reports whether it handled the request; collection
// downloads fall through to the generic first-hit resolution.
//
// The discovery documents are generated. The version list of a collection is
// the union of the members' lists by version string, the first member that
// lists a version describing it, newest first and paginated by the group; the
// collection document is built from that list. A version document comes from
// the first member that has it and its links (own, collection, download) are
// pointed at the group. The collection index is the union of the hosted
// members' indexes: a proxy cannot enumerate its upstream. Every link the
// group hands out is the group's own.
//
// Hosted wins: unless the group merges proxy versions, a collection a readable
// hosted member holds is served by the hosted members only (see
// applyHostedWins), so its version list, version documents and downloads never
// contain a proxy's.
func (h *Handler) serveAnsible(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	p, ok := pkgansible.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return true
	}

	// The root document links relatively and names no origin; every other
	// document carries absolute links (ansible-core uses download_url as sent)
	// on the origin of the request, which its content digest, the validator,
	// therefore holds.
	links := func() pkgansible.Links {
		return pkgansible.Links{Root: h.publicBase(w, r) + "/repos/" + grp.Name + "/api/v3"}
	}

	switch p.Kind {
	case pkgansible.KindRoot:
		h.writeAnsible(w, r, grp, map[string]any{
			"description":        "Galaxy API of an OpenArtifact Ansible collection repository",
			"available_versions": map[string]string{"v3": "v3/"},
		}, false)
	case pkgansible.KindV3:
		l := links()
		h.writeAnsible(w, r, grp, map[string]string{"collections": l.Collections(), "artifacts": l.Artifacts()}, false)
	case pkgansible.KindIndex:
		h.serveAnsibleIndex(w, r, grp, links(), members, be)
	case pkgansible.KindCollection, pkgansible.KindVersions:
		h.serveAnsibleVersions(w, r, grp, links(), p, members, be)
	case pkgansible.KindVersion:
		h.serveAnsibleVersion(w, r, grp, links(), p, relPath, members, be)
	case pkgansible.KindDownload:
		return false
	}

	return true
}

// ansibleListOutcome is what asking a member for a whole list yielded.
type ansibleListOutcome int

const (
	// ansibleListMissing: the member answered 404.
	ansibleListMissing ansibleListOutcome = iota
	// ansibleListFound: the member's list was read to its end.
	ansibleListFound
	// ansibleListFailed: the member failed or answered something unusable.
	ansibleListFailed
)

// ansiblePage is one parsed page of a member's list.
type ansiblePage[T any] struct {
	items []T
	next  string
}

// ansibleMemberRequest is memberGetRequest for the page of a list at offset,
// with the largest page size.
func ansibleMemberRequest(r *http.Request, offset int) *http.Request {
	mr := memberGetRequest(r)
	mr.URL.RawQuery = url.Values{
		"limit":  {strconv.Itoa(pkgansible.MaxLimit)},
		"offset": {strconv.Itoa(offset)},
	}.Encode()

	return mr
}

// collectAnsibleList reads the list at path of member m page by page until its
// last one. A list that cannot be read completely is failed: a partial list
// would silently drop entries.
func collectAnsibleList[T any](
	h *Handler,
	sess *mergeSession,
	r *http.Request,
	m *models.Repository,
	path string,
	be Backend,
	parse func(io.Reader) (ansiblePage[T], error),
) ([]T, ansibleListOutcome) {
	var items []T

	for range ansibleMaxPages {
		a := h.askMember(sess, ansibleMemberRequest(r, len(items)), m, path, pkgansible.MaxDocBytes, be)

		switch {
		case a.failed:
			return nil, ansibleListFailed
		case !a.found && len(items) == 0:
			return nil, ansibleListMissing
		case !a.found:
			return nil, ansibleListFailed
		}

		pg, err := parse(bytes.NewReader(a.body))
		if err != nil {
			return nil, ansibleListFailed
		}

		items = append(items, pg.items...)

		switch {
		case pg.next == "":
			return items, ansibleListFound
		case len(pg.items) == 0 || len(items) > ansibleMaxEntries:
			return nil, ansibleListFailed
		}
	}

	return nil, ansibleListFailed
}

func parseAnsibleVersionPage(rd io.Reader) (ansiblePage[pkgansible.VersionRef], error) {
	pg, err := pkgansible.ParseVersionPage(rd)
	if err != nil {
		return ansiblePage[pkgansible.VersionRef]{}, err //nolint:wrapcheck // the member is just counted as failed
	}

	return ansiblePage[pkgansible.VersionRef]{items: pg.Versions, next: pg.Next}, nil
}

func parseAnsibleIndexPage(rd io.Reader) (ansiblePage[pkgansible.CollectionDoc], error) {
	var doc struct {
		Links struct {
			Next *string `json:"next"`
		} `json:"links"`
		Data []pkgansible.CollectionDoc `json:"data"`
	}

	if err := json.NewDecoder(io.LimitReader(rd, pkgansible.MaxDocBytes)).Decode(&doc); err != nil {
		return ansiblePage[pkgansible.CollectionDoc]{}, err //nolint:wrapcheck // the member is just counted as failed
	}

	pg := ansiblePage[pkgansible.CollectionDoc]{items: doc.Data}
	if doc.Links.Next != nil {
		pg.next = *doc.Links.Next
	}

	return pg, nil
}

// ansibleVersions returns the merged version list of namespace.name over
// members, newest first and linked to the group. failed reports that a member
// could not be read completely, so the list may lack versions it holds.
func (h *Handler) ansibleVersions(
	r *http.Request,
	l pkgansible.Links,
	p pkgansible.Path,
	members []*models.Repository,
	be Backend,
) ([]pkgansible.VersionRef, bool) {
	sess := h.newSession(r.Context())
	defer sess.release()

	var (
		merged []pkgansible.VersionRef
		seen   = map[string]bool{}
		failed bool
	)

	path := "api/v3/collections/" + p.Namespace + "/" + p.Name + "/versions/"

	for _, m := range members {
		refs, outcome := collectAnsibleList(h, sess, r, m, path, be, parseAnsibleVersionPage)

		switch outcome {
		case ansibleListFailed:
			failed = true
		case ansibleListMissing:
		case ansibleListFound:
			for _, v := range refs {
				if !seen[v.Version] {
					seen[v.Version] = true
					merged = append(merged, v)
				}
			}
		}
	}

	pkgansible.NewestFirst(merged)

	for i := range merged {
		merged[i].Relink(l, p.Namespace, p.Name)
	}

	return merged, failed
}

// serveAnsibleVersions answers the collection document and its version list.
func (h *Handler) serveAnsibleVersions(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	l pkgansible.Links,
	p pkgansible.Path,
	members []*models.Repository,
	be Backend,
) {
	limit, offset, err := pkgansible.PageParams(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	refs, failed := h.ansibleVersions(r, l, p, members, be)
	if len(refs) == 0 {
		writeNoDocument(w, r, failed)
		return
	}

	if p.Kind == pkgansible.KindCollection {
		h.writeAnsible(w, r, grp, pkgansible.NewCollectionDoc(l, p.Namespace, p.Name, refs), failed)
		return
	}

	pg := pkgansible.NewPage(l.Versions(p.Namespace, p.Name), len(refs), limit, offset)
	pg.Data = append(make([]pkgansible.VersionRef, 0, min(limit, len(refs))), pkgansible.Window(refs, limit, offset)...)
	h.writeAnsible(w, r, grp, pg, failed)
}

// serveAnsibleVersion answers the document of one version from the first
// member that has a valid one, with its links pointed at the group.
func (h *Handler) serveAnsibleVersion(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	l pkgansible.Links,
	p pkgansible.Path,
	relPath string,
	members []*models.Repository,
	be Backend,
) {
	mr := memberGetRequest(r)

	sess := h.newSession(r.Context())
	defer sess.release()

	failed := false

	for _, m := range members {
		a := h.askMember(sess, mr, m, relPath, pkgansible.MaxDocBytes, be)
		if !a.found {
			failed = failed || a.failed
			continue
		}

		doc, err := pkgansible.ParseVersionDoc(bytes.NewReader(a.body), p.Namespace, p.Name, p.Version)
		if err != nil {
			failed = true
			continue
		}

		sess.release()
		doc.Relink(l)
		h.writeAnsible(w, r, grp, doc, false)

		return
	}

	writeNoDocument(w, r, failed)
}

// serveAnsibleIndex answers the collection index: the union of the hosted
// members' indexes by collection, the first member that lists one describing
// it. Proxy members are not asked, they cannot enumerate their upstream.
func (h *Handler) serveAnsibleIndex(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	l pkgansible.Links,
	members []*models.Repository,
	be Backend,
) {
	limit, offset, err := pkgansible.PageParams(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sess := h.newSession(r.Context())
	defer sess.release()

	var (
		docs   []pkgansible.CollectionDoc
		seen   = map[string]bool{}
		failed bool
	)

	for _, m := range members {
		if m.RepoType == repotype.Proxy {
			continue
		}

		items, outcome := collectAnsibleList(h, sess, r, m, "api/v3/collections/", be, parseAnsibleIndexPage)

		switch outcome {
		case ansibleListFailed:
			failed = true
		case ansibleListMissing:
		case ansibleListFound:
			for _, d := range items {
				key := pkgansible.FQCN(d.Namespace, d.Name)
				if pkgansible.ValidName(d.Namespace) && pkgansible.ValidName(d.Name) && !seen[key] {
					seen[key] = true
					docs = append(docs, relinkAnsibleCollection(l, d))
				}
			}
		}
	}

	sess.release()

	if len(docs) == 0 && failed {
		writeNoDocument(w, r, true)
		return
	}

	slices.SortFunc(docs, func(a, b pkgansible.CollectionDoc) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})

	pg := pkgansible.NewPage(l.Collections(), len(docs), limit, offset)
	pg.Data = append(make([]pkgansible.CollectionDoc, 0, min(limit, len(docs))), pkgansible.Window(docs, limit, offset)...)
	h.writeAnsible(w, r, grp, pg, failed)
}

// relinkAnsibleCollection points the links of a member's collection document at l.
func relinkAnsibleCollection(l pkgansible.Links, d pkgansible.CollectionDoc) pkgansible.CollectionDoc {
	d.Href = l.Collection(d.Namespace, d.Name)
	d.VersionsURL = l.Versions(d.Namespace, d.Name)

	if d.HighestVersion != nil {
		d.HighestVersion = &pkgansible.VersionTag{
			Href:    l.Version(d.Namespace, d.Name, d.HighestVersion.Version),
			Version: d.HighestVersion.Version,
		}
	}

	return d
}

// writeAnsible answers a generated document with a content ETag, honoring
// If-None-Match; partial (a member failed while it was assembled) sends it
// uncacheable and without validator.
func (h *Handler) writeAnsible(w http.ResponseWriter, r *http.Request, grp *models.Repository, doc any, partial bool) {
	body, err := json.Marshal(doc)
	if err != nil {
		h.log.Errorw("failed to encode ansible document", "group", grp.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	writeMerged(w, r, grp.Public, body, "application/json", base.ContentETag(body), ansibleMetaMaxAge, partial)
}
