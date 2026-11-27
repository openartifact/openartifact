// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgcran "github.com/openartifact/openartifact/pkg/cran"
)

const (
	// maxCRANIndexBytes bounds one member's PACKAGES held in memory (CRAN's
	// source index is about 8 MiB, the largest binary ones a bit more).
	maxCRANIndexBytes = pkgcran.MaxIndexBytes

	// maxCRANNamesBytes bounds one hosted member's package name listing.
	maxCRANNamesBytes = 64 << 20

	// cranIndexMaxAge is how long shared caches may reuse a public group's
	// PACKAGES before revalidating (as for hosted cran repositories).
	cranIndexMaxAge = 60

	cranArchiveRoot = pkgcran.SourceTree + "/" + pkgcran.ArchiveDir + "/"
)

// CRANHooks is the cran group: merged PACKAGES indexes, package files first-hit.
var CRANHooks = Hooks{ //nolint:gochecknoglobals // immutable format hook value
	ServeMerged: (*Handler).serveCRAN,
	PackageRef:  cranRef,
}

// serveCRAN answers the PACKAGES, PACKAGES.gz and PACKAGES.rds of a tree with
// the union of the members' indexes. Package files and the Archive listing of
// one package fall through to the first-hit resolution (hosted wins is applied
// before); every other path is 404, which also keeps a hosted member's list of
// package names (src/contrib/Archive/) from being exposed through the group.
//
// Merge rules: an index lists one version per package, so of the members that
// list a package the highest version wins (the earliest member on a tie), and
// the record is the member's own, MD5sum included, so the client's download
// (first-hit, by file name) finds the file the MD5 describes. Unless the group
// merges proxy versions, a package any hosted member holds (in any tree) is
// left out of the proxy members' indexes: only the hosted versions of it are
// published. A hosted member whose names cannot be read fails the request (502)
// while a proxy is in play, since they may be the names that must be hidden.
func (h *Handler) serveCRAN(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	relPath string,
	members []*models.Repository,
	be Backend,
) bool {
	p, ok := pkgcran.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return true
	}

	switch p.Kind {
	case pkgcran.KindPackage, pkgcran.KindArchivePackage, pkgcran.KindArchiveDir:
		return false
	case pkgcran.KindIndex:
	case pkgcran.KindArchiveRoot, pkgcran.KindOther:
		http.NotFound(w, r)
		return true
	}

	hidden, ok := h.cranHiddenNames(w, r, grp, members, be)
	if !ok {
		return true
	}

	h.serveMergedDoc(w, r, grp, members, be, mergedDoc{
		ask:         memberAsk{path: samePath(p.Tree.Dir + "/PACKAGES"), limit: int(maxCRANIndexBytes)},
		what:        "cran indexes",
		maxAge:      cranIndexMaxAge,
		contentType: p.Variant.ContentType(),
		merge: func(c *collection) ([]byte, error) {
			return h.mergeCRANIndexes(grp, c, hidden, p)
		},
	})

	return true
}

// cranHiddenNames is the set of package names the hosted members hold: the
// proxy members' entries of those names are left out of the merged index. It is
// nil when nothing is hidden: the group merges proxy versions, or lacks a hosted
// or a proxy member. ok is false when a hosted member's names could not be read
// (502 written).
func (h *Handler) cranHiddenNames(
	w http.ResponseWriter,
	r *http.Request,
	grp *models.Repository,
	readable []*models.Repository,
	be Backend,
) (map[string]struct{}, bool) {
	hosted, hasProxy := splitHosted(readable)
	if grp.MergeProxyVersions || len(hosted) == 0 || !hasProxy {
		return nil, true
	}

	sess := h.newSession(r.Context())
	defer sess.release()

	c := h.collect(r, sess, hosted, memberAsk{path: samePath(cranArchiveRoot), limit: maxCRANNamesBytes}, be)
	if c.partial() {
		http.Error(w, "member repository error", http.StatusBadGateway)
		return nil, false
	}

	hidden := map[string]struct{}{}

	for _, d := range c.docs {
		for _, entry := range pkgcran.Listing(d.body) {
			if name, ok := strings.CutSuffix(entry, "/"); ok && name != "" {
				hidden[name] = struct{}{}
			}
		}
	}

	return hidden, true
}

// mergeCRANIndexes merges the members' PACKAGES of one tree and renders the
// variant p asks for.
func (h *Handler) mergeCRANIndexes(grp *models.Repository, c *collection, hidden map[string]struct{}, p pkgcran.Path) ([]byte, error) {
	best := map[string]pkgcran.Record{}
	usable := 0

	for _, d := range c.docs {
		recs, err := pkgcran.ParsePackages(bytes.NewReader(d.body), maxCRANIndexBytes)
		if err != nil {
			h.log.Warnw("member cran index unreadable", "group", grp.Name, "member", d.member.Name, "error", err)
			c.fail(d.member)

			continue
		}

		usable++

		for _, rec := range recs {
			name := rec.Get("Package")
			if name == "" {
				continue
			}

			if _, held := hidden[name]; held && d.member.RepoType == repotype.Proxy {
				continue
			}

			if cur, ok := best[name]; !ok || pkgcran.Less(cur.Get("Version"), rec.Get("Version")) {
				best[name] = rec
			}
		}
	}

	if usable == 0 {
		return nil, errNoDocument
	}

	names := make([]string, 0, len(best))
	for name := range best {
		names = append(names, name)
	}

	slices.Sort(names)

	recs := make([]pkgcran.Record, len(names))
	for i, name := range names {
		recs[i] = best[name]
	}

	switch p.Variant {
	case pkgcran.VariantGzip:
		return pkgcran.Gzip(pkgcran.RenderText(recs)) //nolint:wrapcheck // caller logs
	case pkgcran.VariantRDS:
		return pkgcran.RenderRDS(recs, p.Tree.Binary) //nolint:wrapcheck // caller logs
	case pkgcran.VariantText:
	}

	return pkgcran.RenderText(recs), nil
}

func cranRef(relPath string) (PkgRef, bool) {
	p, ok := pkgcran.ParsePath(relPath)
	if !ok {
		return PkgRef{}, false
	}

	switch p.Kind {
	case pkgcran.KindPackage, pkgcran.KindArchivePackage:
		return PkgRef{probe: cranProbe(p.Package), file: true}, true
	case pkgcran.KindArchiveDir:
		return PkgRef{probe: cranProbe(p.Package)}, true
	case pkgcran.KindIndex, pkgcran.KindArchiveRoot, pkgcran.KindOther:
	}

	return PkgRef{}, false
}

// cranProbe is the document a hosted cran member answers 200 for iff it holds
// the package in any tree.
func cranProbe(pkg string) string {
	return fmt.Sprintf("%s%s/", cranArchiveRoot, pkg)
}
