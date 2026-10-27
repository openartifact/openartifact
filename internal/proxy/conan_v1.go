// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgconan "github.com/openartifact/openartifact/pkg/conan"
)

// A Conan 1 client (revisions disabled, its default) asks the remote for a map
// of file name to URL and transfers each file from that URL. The proxy answers
// it from the Conan 2 documents of the upstream, which every Conan remote that
// serves revisions has, so the files are cached once for both kinds of client:
//
//   - download_urls and digest name the files of the newest revision (found with
//     the cached "latest" and file list documents) at v1/files/... of this
//     repository, and a request for such a URL is the Conan 2 file request;
//   - the searches and the snapshot (file to MD5, which the revision documents
//     do not carry) are passed through to the upstream's own Conan 1 endpoints.
func (s *Service) serveConanV1(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgconan.V1Path) {
	switch p.Kind {
	case pkgconan.V1Search:
		s.serveConanPassthrough(w, r, repo, p.String(), checkSearch)
	case pkgconan.V1PackageSearch:
		s.serveConanPassthrough(w, r, repo, p.String(), checkPackageSearch)
	case pkgconan.V1Snapshot, pkgconan.V1PackageSnapshot:
		s.serveConanPassthrough(w, r, repo, p.String(), checkFileMap)
	case pkgconan.V1DownloadURLs, pkgconan.V1PackageDownloadURLs:
		s.serveConanV1URLs(w, r, repo, p, false)
	case pkgconan.V1Digest, pkgconan.V1PackageDigest:
		s.serveConanV1URLs(w, r, repo, p, true)
	case pkgconan.V1File, pkgconan.V1PackageFile:
		if v2, ok := p.V2File(); ok {
			s.serveConanCached(w, r, repo, v2)
		}
	case pkgconan.V1Other:
		http.NotFound(w, r)
	}
}

// serveConanV1URLs answers {file: url} for the files of the newest revision of
// the recipe, or of the package when p names one; only the manifest when
// manifestOnly.
func (s *Service) serveConanV1URLs(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgconan.V1Path, manifestOnly bool) {
	ctx := r.Context()
	files := pkgconan.Path{Kind: pkgconan.KindFiles, Ref: p.Ref, PID: p.PID}

	latest, err := s.conanRevision(ctx, repo, pkgconan.Path{Kind: pkgconan.KindRecipeLatest, Ref: p.Ref})
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	files.RRev = latest

	if p.PID != "" {
		files.PRev, err = s.conanRevision(ctx, repo, pkgconan.Path{
			Kind: pkgconan.KindPackageLatest, Ref: p.Ref, RRev: latest, PID: p.PID,
		})
		if err != nil {
			s.writeResolveError(w, r, err)
			return
		}
	}

	doc, err := s.conanDocument(ctx, repo, files)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	names, _, err := pkgconan.ParseFiles(doc)
	if err != nil {
		s.log.Warnw("unreadable cached conan file list", "repo", repo.Name, "path", files.String())
		s.writeResolveError(w, r, errVerify)

		return
	}

	base := pkgconan.RepoURL(s.baseURL, repo.Name)
	urls := make(map[string]string, len(names))

	for _, name := range names {
		if !manifestOnly || name == pkgconan.ManifestFile {
			urls[name] = pkgconan.V1FileURL(base, p.Ref, files.RRev, p.PID, files.PRev, name)
		}
	}

	if len(urls) == 0 {
		http.NotFound(w, r)
		return
	}

	writeConanJSON(w, r, pkgconan.RenderFileMap(urls))
}

// conanRevision is the revision a latest document names.
func (s *Service) conanRevision(ctx context.Context, repo *models.Repository, p pkgconan.Path) (string, error) {
	doc, err := s.conanDocument(ctx, repo, p)
	if err != nil {
		return "", err
	}

	rev, err := pkgconan.ParseRevision(doc)
	if err != nil {
		s.log.Warnw("unreadable cached conan revision", "repo", repo.Name, "path", p.String())
		return "", errVerify
	}

	return rev.ID, nil
}

// conanDocument returns the (cached) document of p as the proxy would serve it.
func (s *Service) conanDocument(ctx context.Context, repo *models.Repository, p pkgconan.Path) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/"+p.String(), nil)
	if err != nil {
		return nil, errInternal
	}

	dw := &docWriter{header: make(http.Header)}
	s.serveConanCached(dw, req, repo, p)

	switch dw.status {
	case 0, http.StatusOK:
		return dw.body.Bytes(), nil
	case http.StatusNotFound:
		return nil, errNotFound
	}

	return nil, errUpstream
}

// docWriter collects a response in memory.
type docWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (d *docWriter) Header() http.Header { return d.header }

func (d *docWriter) WriteHeader(code int) {
	if d.status == 0 {
		d.status = code
	}
}

func (d *docWriter) Write(b []byte) (int, error) {
	d.WriteHeader(http.StatusOK)
	return d.body.Write(b) //nolint:wrapcheck // bytes.Buffer never fails
}
