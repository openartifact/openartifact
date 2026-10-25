// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"path"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocipath"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/pypisimple"
	"github.com/openartifact/openartifact/internal/repotype"
	pkgcargo "github.com/openartifact/openartifact/pkg/cargo"
	pkghelm "github.com/openartifact/openartifact/pkg/helm"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

// Hosted wins is the group's defence against dependency confusion: when an
// internal package name is also published upstream, a group of a hosted and a
// proxy member must not let the proxy's (often higher) versions or files of
// that name reach a client. Unless Repository.MergeProxyVersions is set, a
// package name that any readable hosted member holds is served from the hosted
// members only; proxies keep serving every name no hosted member holds. A
// hosted member the caller cannot read is absent (the list passed in is the
// already authorized one), so it neither owns a name nor hides the proxies.
//
// A hosted member "holds" a name when it answers a package-level document with
// 200 (404 means it does not). The document, per format:
//
//	npm     <name>, scoped as @scope%2fname      (the packument)
//	pypi    simple/<project>/                    (project page; files too)
//	go      <module>/@v/list                     (200 with at least a version)
//	cargo   index/<path of the crate>            (downloads: the lowercased crate)
//	nuget   v3/package/<id>/index.json           (registrations and flat container)
//	maven   <g>/<a>[/<v>]/maven-metadata.xml     (file paths: <g>/<a>/maven-metadata.xml)
//	helm    index.yaml of each hosted member     (charts/<file> only; index.yaml is
//	                                              filtered in serveHelm instead)
//	docker  <image>/tags/list                    (manifests, blobs and tags)
//	raw     none: no versions are merged, the first member in order wins.
//
// Requests for a file of an owned name are answered by the hosted members
// alone: a file no hosted member has is 404 (502 if one failed), never a
// proxy's. Requests for package metadata of an owned name are merged over the
// hosted members only. If no hosted member owns the name but one could not be
// asked (5xx), the request fails closed with 502: serving the proxies then
// could hand out the very versions that must be hidden.
//
// Names that only index lists (pypi simple/ index, the nuget service index,
// cargo config.json) are merged unchanged: they reveal no versions.

// pkgRef is the package a request names and how to find out whether a hosted
// member holds it.
type pkgRef struct {
	// probe is the member path of a package-level document a hosted member
	// answers 200 for iff it holds the package.
	probe string
	// accept is the Accept header the probe is made with, "" to keep the
	// client's.
	accept string
	// nonEmpty requires the 200 probe answer to carry a body (go: an empty
	// version list means the module has no versions).
	nonEmpty bool
	// chartFile is set for a helm chart download: ownership is then decided
	// from the hosted members' index.yaml, not from a probe document.
	chartFile string
	// file is true for a request for a file of the package rather than for
	// its metadata.
	file bool
	// pathFor is the member path a file request is made with.
	pathFor func(*models.Repository) string
}

// packageRef extracts the package a request of a group of format names. ok is
// false for requests that name none (indexes, config documents) and for
// formats that have no versions to hide (raw).
func packageRef(format, relPath string) (pkgRef, bool) {
	var (
		ref pkgRef
		ok  bool
	)

	switch format {
	case "npm":
		ref, ok = npmRef(relPath)
	case "pypi":
		ref, ok = pypiRef(relPath)
	case "go":
		ref, ok = goRef(relPath)
	case "cargo":
		ref, ok = cargoRef(relPath)
	case "nuget":
		ref, ok = nugetRef(relPath)
	case "maven":
		ref, ok = mavenRef(relPath)
	case "helm":
		ref, ok = helmRef(relPath)
	}

	if !ok {
		return pkgRef{}, false
	}

	if ref.pathFor == nil {
		ref.pathFor = func(*models.Repository) string { return relPath }
	}

	return ref, true
}

func npmRef(relPath string) (pkgRef, bool) {
	p, err := pkgnpm.ParsePath(relPath)
	if err != nil {
		return pkgRef{}, false
	}

	return pkgRef{
		probe:  strings.Replace(p.Package, "/", "%2f", 1),
		accept: "application/json",
		file:   p.Kind == pkgnpm.KindTarball,
	}, true
}

func pypiRef(relPath string) (pkgRef, bool) {
	kind, project, filename := pypisimple.ParsePath(relPath)

	switch kind {
	case pypisimple.PathProject:
		return pkgRef{probe: "simple/" + project + "/", accept: pypiMemberAccept}, true
	case pypisimple.PathFile:
		return pkgRef{
			probe:  "simple/" + project + "/",
			accept: pypiMemberAccept,
			file:   true,
			pathFor: func(m *models.Repository) string {
				return pypiFilePath(m, project, filename)
			},
		}, true
	case pypisimple.PathIndex, pypisimple.PathOther:
	}

	return pkgRef{}, false
}

func goRef(relPath string) (pkgRef, bool) {
	rel := strings.TrimPrefix(relPath, "/")

	mod, _, ok := strings.Cut(rel, "/@v/")
	if !ok {
		mod, ok = strings.CutSuffix(rel, "/@latest")
	}

	if !ok || mod == "" {
		return pkgRef{}, false
	}

	return pkgRef{probe: mod + "/@v/list", nonEmpty: true, file: rel != mod+"/@v/list"}, true
}

func cargoRef(relPath string) (pkgRef, bool) {
	p, err := pkgcargo.ParsePath(relPath)
	if err != nil {
		return pkgRef{}, false
	}

	switch p.Kind {
	case pkgcargo.KindIndex:
		return pkgRef{probe: "index/" + p.IndexPath}, true
	case pkgcargo.KindDownload:
		return pkgRef{probe: "index/" + pkgcargo.IndexPathFor(strings.ToLower(p.Crate)), file: true}, true
	case pkgcargo.KindConfig:
	}

	return pkgRef{}, false
}

func nugetRef(relPath string) (pkgRef, bool) {
	segs := strings.Split(strings.Trim(relPath, "/"), "/")
	if len(segs) < 3 || segs[0] != "v3" || (segs[1] != "registration" && segs[1] != "package") || !nugetSegment(segs[2]) {
		return pkgRef{}, false
	}

	return pkgRef{
		probe:  "v3/package/" + strings.ToLower(segs[2]) + "/index.json",
		accept: "application/json",
		file:   strings.HasSuffix(relPath, ".nupkg") || strings.HasSuffix(relPath, ".nuspec"),
	}, true
}

func mavenRef(relPath string) (pkgRef, bool) {
	if doc, _, ok := mavenMetadataPath(relPath); ok {
		return pkgRef{probe: doc}, true
	}

	rel := strings.TrimPrefix(relPath, "/")

	// A signature or the like of the metadata document belongs to that
	// document's package.
	if strings.HasPrefix(path.Base(rel), "maven-metadata.xml") {
		return pkgRef{probe: path.Dir(rel) + "/maven-metadata.xml", file: true}, true
	}

	// <group path>/<artifact>/<version>/<file>: the artifact's metadata is
	// two levels up from the file.
	if strings.Count(rel, "/") < 3 {
		return pkgRef{}, false
	}

	return pkgRef{probe: path.Dir(path.Dir(rel)) + "/maven-metadata.xml", file: true}, true
}

func helmRef(relPath string) (pkgRef, bool) {
	file, ok := strings.CutPrefix(strings.TrimPrefix(relPath, "/"), helmChartsDir)
	if !ok || !pathseg.Name(file) {
		return pkgRef{}, false
	}

	return pkgRef{chartFile: file, file: true}, true
}

// ociRef is the image a docker request names. Referrers are files of the image
// like manifests and blobs.
func ociRef(req ocipath.Request, relPath string) (pkgRef, bool) {
	var file bool

	switch req.Kind { //nolint:exhaustive // uploads are refused before this point
	case ocipath.KindManifest, ocipath.KindBlob, ocipath.KindReferrers:
		file = true
	case ocipath.KindTags:
	default:
		return pkgRef{}, false
	}

	probe := "tags/list"
	if _, image := ocipath.SplitName(req.Name); image != "" {
		probe = image + "/tags/list"
	}

	return pkgRef{
		probe:   probe,
		file:    file,
		pathFor: func(*models.Repository) string { return relPath },
	}, true
}

// replies is how a hosted-wins decision is told to the client, which differs
// between plain HTTP formats and the registry protocol.
type replies struct {
	unavailable func(w http.ResponseWriter)
	notFound    func(w http.ResponseWriter, r *http.Request)
}

func httpReplies() replies {
	return replies{
		unavailable: func(w http.ResponseWriter) {
			http.Error(w, "member repository error", http.StatusBadGateway)
		},
		notFound: func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
	}
}

// applyHostedWins narrows readable for the package relPath names: the members
// to resolve the request against. done is true when it has answered w itself.
// See the comment above for the rules; it is a no-op for groups that merge
// proxy versions, groups without both a hosted and a proxy member, and
// requests that name no package.
func (h *Handler) applyHostedWins(w http.ResponseWriter, r *http.Request, grp *models.Repository, readable []*models.Repository, relPath string, be Backend) ([]*models.Repository, bool) {
	if grp.MergeProxyVersions {
		return readable, false
	}

	ref, ok := packageRef(grp.Format, relPath)
	if !ok {
		return readable, false
	}

	return h.resolveHostedWins(w, r, readable, ref, be, httpReplies())
}

// applyHostedWinsOCI is applyHostedWins for a docker group, answering in the
// registry's error format.
func (h *Handler) applyHostedWinsOCI(w http.ResponseWriter, r *http.Request, grp *models.Repository, readable []*models.Repository, req ocipath.Request, relPath string, be Backend) ([]*models.Repository, bool) {
	if grp.MergeProxyVersions {
		return readable, false
	}

	ref, ok := ociRef(req, relPath)
	if !ok {
		return readable, false
	}

	return h.resolveHostedWins(w, r, readable, ref, be, replies{
		unavailable: func(w http.ResponseWriter) {
			ocipath.WriteError(w, http.StatusBadGateway, "UNAVAILABLE", "member repository error")
		},
		notFound: func(w http.ResponseWriter, _ *http.Request) {
			code, msg := ocipath.NotFoundCode(req.Kind)
			ocipath.WriteError(w, http.StatusNotFound, code, msg)
		},
	})
}

// ownership is what asking a hosted member about a package yielded.
type ownership int

const (
	ownsNot     ownership = iota // the member does not hold the package
	ownsYes                      // it does
	ownsUnknown                  // it failed to say
)

func (h *Handler) resolveHostedWins(w http.ResponseWriter, r *http.Request, readable []*models.Repository, ref pkgRef, be Backend, rep replies) ([]*models.Repository, bool) {
	hosted, hasProxy := splitHosted(readable)
	if len(hosted) == 0 || !hasProxy {
		return readable, false
	}

	// A file the hosted members have is theirs, whatever the proxies hold.
	hostedFailed := false

	if ref.file {
		served, failed := firstHit(w, r, hosted, ref.pathFor, be)
		if served {
			return nil, true
		}

		hostedFailed = failed
	}

	mr := probeRequest(r, ref)
	owned, unknown := false, false

	for _, m := range hosted {
		switch h.owns(mr, m, ref, be) {
		case ownsYes:
			owned = true
		case ownsUnknown:
			unknown = true
		case ownsNot:
		}

		if owned {
			break
		}
	}

	switch {
	case owned && ref.file:
		// The hosted members hold the package but not this file.
		if hostedFailed {
			rep.unavailable(w)
		} else {
			rep.notFound(w, r)
		}

		return nil, true
	case owned:
		return hosted, false
	case unknown:
		// A member that may hold the name did not answer: serving the proxies
		// could hand out what must stay hidden.
		rep.unavailable(w)
		return nil, true
	}

	return readable, false
}

// splitHosted returns the hosted members of readable and whether it has a
// proxy member. Everything that is not a proxy is hosted.
func splitHosted(readable []*models.Repository) ([]*models.Repository, bool) {
	hosted := make([]*models.Repository, 0, len(readable))
	hasProxy := false

	for _, m := range readable {
		if m.RepoType == repotype.Proxy {
			hasProxy = true
		} else {
			hosted = append(hosted, m)
		}
	}

	return hosted, hasProxy
}

// probeRequest is the request hosted members are asked about a package with: a
// plain GET for the probe document, without the client's conditional or range
// headers and query.
func probeRequest(r *http.Request, ref pkgRef) *http.Request {
	mr := r.Clone(r.Context())
	mr.Method = http.MethodGet
	mr.URL.RawQuery = ""

	for _, hdr := range []string{"If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range", "Range"} {
		mr.Header.Del(hdr)
	}

	if ref.accept != "" {
		mr.Header.Set("Accept", ref.accept)
	}

	return mr
}

// owns asks hosted member m whether it holds the package of ref.
func (h *Handler) owns(mr *http.Request, m *models.Repository, ref pkgRef, be Backend) ownership {
	if ref.chartFile != "" {
		return h.ownsChart(mr, m, ref.chartFile, be)
	}

	sw := &statusWriter{header: make(http.Header)}
	be.ServeMember(sw, mr, m, ref.probe)

	// A handler that wrote nothing answers an empty 200, as net/http does.
	if sw.status == 0 {
		sw.status = http.StatusOK
	}

	switch {
	case sw.status == http.StatusNotFound:
		return ownsNot
	case sw.status == http.StatusOK && (!ref.nonEmpty || sw.written > 0):
		return ownsYes
	case sw.status == http.StatusOK:
		return ownsNot
	}

	return ownsUnknown
}

// ownsChart reports whether hosted helm member m lists a chart that file (a
// chart package or its .prov) belongs to.
func (h *Handler) ownsChart(mr *http.Request, m *models.Repository, file string, be Backend) ownership {
	sess := h.newSession(mr.Context())
	defer sess.release()

	cw := sess.capture(maxHelmIndexBytes)
	be.ServeMember(cw, mr, m, "index.yaml")

	switch {
	case cw.status == http.StatusNotFound:
		return ownsNot
	case cw.status != http.StatusOK || cw.overflow:
		return ownsUnknown
	}

	ix, err := pkghelm.ParseIndex(cw.body.Bytes())
	if err != nil {
		h.log.Warnw("member helm index unreadable", "member", m.Name, "error", err)
		return ownsUnknown
	}

	for cv := range ix.Versions() {
		if isChartFileOf(cv.Name, file) {
			return ownsYes
		}
	}

	return ownsNot
}

// isChartFileOf reports whether file is named like a file of chart name:
// "<name>-<version>" with a version starting with a digit or v<digit>.
func isChartFileOf(name, file string) bool {
	rest, ok := strings.CutPrefix(file, name+"-")
	if !ok || rest == "" {
		return false
	}

	if rest[0] == 'v' {
		rest = rest[1:]
	}

	return rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

// statusWriter records the status and the amount of the body a member wrote and
// drops the body: a probe only needs to know whether the member has the
// document.
type statusWriter struct {
	header  http.Header
	status  int
	written int
}

func (s *statusWriter) Header() http.Header { return s.header }

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.WriteHeader(http.StatusOK)
	s.written += len(b)

	return len(b), nil
}
