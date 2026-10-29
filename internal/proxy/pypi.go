// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/pypisimple"
)

const (
	// maxPyPIPageBytes bounds one index or project page held in memory
	// (the full pypi.org index is a few tens of MiB).
	maxPyPIPageBytes = 256 << 20

	// pypiPageMaxAge is how long shared caches may reuse a public repository's
	// simple pages before revalidating (same as hosted pypi repositories).
	pypiPageMaxAge = 60

	pypiIndexKey = "simple/"
)

var errBadPage = errors.New("unparsable upstream simple page")

// servePyPI answers a GET/HEAD for a pypi proxy repository.
func (s *Service) servePyPI(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	kind, project, filename := pypisimple.ParsePath(relPath)

	switch kind {
	case pypisimple.PathIndex:
		s.servePyPIIndex(w, r, repo)
	case pypisimple.PathProject:
		s.servePyPIProject(w, r, repo, project)
	case pypisimple.PathFile:
		s.servePyPIFile(w, r, repo, project, filename)
	case pypisimple.PathOther:
		http.NotFound(w, r)
	}
}

// pageETag identifies one rendering of a cached page: the upstream body, the
// repository (links embed its name), the path and the serialization.
func pageETag(repo *models.Repository, key, blobKey string, offer pypisimple.Offer) string {
	sum := sha256.Sum256([]byte(repo.Name + "\x00" + key + "\x00" + blobKey + "\x00" + offer.ContentType))

	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

func pypiProjectKey(project string) string { return "simple/" + project + "/" }

func pypiFileKey(project, filename string) string { return "files/" + project + "/" + filename }

// pypiPageDoc is the upstream simple page at path below the upstream URL ("" is
// the index), cached under key. An unparsable page is dropped from the cache.
func pypiPageDoc(key, path string) metaDoc {
	return metaDoc{
		key:   key,
		url:   func(upstream string) (string, error) { return upstreamURL(upstream, path) },
		opts:  staticOpts(fetchOpts{accept: pypisimple.UpstreamAccept}),
		limit: maxPyPIPageBytes,
		what:  "simple page",
		bad:   badDocDrop,
	}
}

func (s *Service) servePyPIIndex(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	offer, ok := pypisimple.Negotiate(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "no acceptable simple API serialization", http.StatusNotAcceptable)
		return
	}

	s.servePyPIPage(w, r, repo, offer, pypiPageDoc(pypiIndexKey, ""), func(body []byte, contentType string) ([]byte, error) {
		names, perr := pypisimple.ParseIndex(body, contentType)
		if perr != nil {
			return nil, fmt.Errorf("%w: %w", errUnusableDoc, perr)
		}

		return pypisimple.RenderIndex(offer.Format, names, func(name string) string {
			return pypisimple.ProjectURL(repo.Name, name)
		})
	})
}

func (s *Service) servePyPIProject(w http.ResponseWriter, r *http.Request, repo *models.Repository, project string) {
	offer, ok := pypisimple.Negotiate(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "no acceptable simple API serialization", http.StatusNotAcceptable)
		return
	}

	s.servePyPIPage(
		w,
		r,
		repo,
		offer,
		pypiPageDoc(pypiProjectKey(project), project+"/"),
		func(body []byte, contentType string) ([]byte, error) {
			files, perr := pypisimple.ParseProject(body, contentType)
			if perr != nil {
				return nil, fmt.Errorf("%w: %w", errUnusableDoc, perr)
			}

			return pypisimple.RenderProject(offer.Format, project, pypisimple.ServableFiles(files), func(f *pypisimple.File) string {
				return pypisimple.FileURL(repo.Name, project, f.Filename)
			})
		},
	)
}

// servePyPIPage answers one rewritten simple page from the cached upstream
// page in the negotiated serialization.
func (s *Service) servePyPIPage(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	offer pypisimple.Offer,
	doc metaDoc,
	render func(body []byte, contentType string) ([]byte, error),
) {
	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: doc,
		maxAge:  pypiPageMaxAge,
		vary:    "Accept",
		etag:    func(blobKey string) string { return pageETag(repo, doc.key, blobKey, offer) },
		render: func(body []byte, res *resolution) ([]byte, string, error) {
			out, err := render(body, res.entry.ContentType)
			return out, offer.ContentType, err
		},
	})
}

func (s *Service) servePyPIFile(w http.ResponseWriter, r *http.Request, repo *models.Repository, project, filename string) {
	s.serveDownload(w, r, repo, pypiFileKey(project, filename), func(ctx context.Context) (download, error) {
		return s.locatePyPIFile(ctx, repo, project, filename)
	})
}

// locatePyPIFile finds a distribution file (or its PEP 658 .metadata) where
// the cached project page says it lives, with the published sha256 it must
// have. A file the page does not list is not found.
func (s *Service) locatePyPIFile(ctx context.Context, repo *models.Repository, project, filename string) (download, error) {
	target, err := upstreamURL(repo.UpstreamURL, project+"/")
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return download{}, errInternal
	}

	// Relative file links resolve against the URL the page was fetched from.
	pageURL, err := url.Parse(target)
	if err != nil {
		return download{}, errInternal
	}

	files, _, err := parseDoc(ctx, s, repo, pypiPageDoc(pypiProjectKey(project), project+"/"),
		func(body []byte, res *resolution) ([]pypisimple.File, error) {
			return pypisimple.ParseProject(body, res.entry.ContentType) //nolint:wrapcheck // logged by parseDoc
		})
	if err != nil {
		return download{}, err
	}

	wantMeta := false
	name := filename

	if stem, ok := strings.CutSuffix(filename, pypisimple.MetadataSuffix); ok {
		name, wantMeta = stem, true
	}

	var file *pypisimple.File

	for i := range files {
		if files[i].Filename == name {
			file = &files[i]
			break
		}
	}

	if file == nil || (wantMeta && !file.CoreMetadata) {
		return download{}, errNotFound
	}

	key := pypiFileKey(project, filename)

	ref, err := url.Parse(file.URL)
	if err != nil {
		s.log.Warnw("upstream simple page has an invalid file link", "repo", repo.Name, "path", key, "error", err)
		return download{}, errUpstream
	}

	dl := pageURL.ResolveReference(ref)
	if dl.Scheme != "http" && dl.Scheme != "https" {
		s.log.Warnw("upstream simple page links a non-http file", "repo", repo.Name, "path", key, "scheme", dl.Scheme)
		return download{}, errUpstream
	}

	dl.Fragment = ""
	want := file.SHA256()

	if wantMeta {
		dl.Path += pypisimple.MetadataSuffix
		dl.RawPath = ""
		want = file.MetadataSHA256()
	}

	return download{url: dl.String(), opts: fetchOpts{wantSHA256: want, unverified: want == ""}}, nil
}
