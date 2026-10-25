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
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
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

// pageRender is one simple page being answered: the negotiated serialization,
// and how to turn the parsed upstream body into the response.
type pageRender struct {
	offer  pypisimple.Offer
	key    string
	target string
	render func(body []byte, contentType string) ([]byte, error)
}

func (s *Service) servePyPIIndex(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	offer, ok := pypisimple.Negotiate(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "no acceptable simple API serialization", http.StatusNotAcceptable)
		return
	}

	target, err := upstreamURL(repo.UpstreamURL, "")
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	s.servePyPIPage(w, r, repo, pageRender{
		offer:  offer,
		key:    pypiIndexKey,
		target: target,
		render: func(body []byte, contentType string) ([]byte, error) {
			names, perr := pypisimple.ParseIndex(body, contentType)
			if perr != nil {
				return nil, fmt.Errorf("%w: %w", errBadPage, perr)
			}

			return pypisimple.RenderIndex(offer.Format, names, func(name string) string {
				return pypisimple.ProjectURL(repo.Name, name)
			})
		},
	})
}

func (s *Service) servePyPIProject(w http.ResponseWriter, r *http.Request, repo *models.Repository, project string) {
	offer, ok := pypisimple.Negotiate(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "no acceptable simple API serialization", http.StatusNotAcceptable)
		return
	}

	target, err := upstreamURL(repo.UpstreamURL, project+"/")
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	s.servePyPIPage(w, r, repo, pageRender{
		offer:  offer,
		key:    pypiProjectKey(project),
		target: target,
		render: func(body []byte, contentType string) ([]byte, error) {
			files, perr := pypisimple.ParseProject(body, contentType)
			if perr != nil {
				return nil, fmt.Errorf("%w: %w", errBadPage, perr)
			}

			return pypisimple.RenderProject(offer.Format, project, pypisimple.ServableFiles(files), func(f *pypisimple.File) string {
				return pypisimple.FileURL(repo.Name, project, f.Filename)
			})
		},
	})
}

func pypiProjectKey(project string) string { return "simple/" + project + "/" }

func pypiFileKey(project, filename string) string { return "files/" + project + "/" + filename }

// fetchPage resolves a simple page through the cache (TTL, stale-on-error).
func (s *Service) fetchPage(ctx context.Context, repo *models.Repository, key, target string) (*resolution, error) {
	return s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchURL(fctx, repo, key, target, fetchOpts{accept: pypisimple.UpstreamAccept})
	})
}

// readPage loads a cached page body, holding one of the bounded in-memory
// document slots; the caller must call release once it is done parsing and
// rendering the body (release is nil when err != nil). When the cache row
// outlived its blob the row is dropped and the page refetched once, with the
// slot given up for the duration of the network fetch.
func (s *Service) readPage(ctx context.Context, repo *models.Repository, key, target string, res *resolution) (*resolution, []byte, func(), error) {
	if err := s.packuments.Acquire(ctx); err != nil {
		return nil, nil, nil, err //nolint:wrapcheck // caller checks for cancellation
	}

	body, err := s.readBlob(ctx, res.entry.BlobKey)
	if err == nil {
		return res, body, s.packuments.Release, nil
	}

	s.packuments.Release()

	if !res.fromCache {
		s.log.Errorw("failed to read fetched simple page", "repo", repo.Name, "path", key, "error", err)
		return nil, nil, nil, errInternal
	}

	s.log.Warnw("proxy cache blob missing, refetching", "repo", repo.Name, "path", key, "error", err)

	if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, key); derr != nil {
		s.log.Errorw("failed to drop stale proxy cache entry", "repo", repo.Name, "path", key, "error", derr)
	}

	res, err = s.fetchPage(ctx, repo, key, target)
	if err != nil {
		return nil, nil, nil, err
	}

	if aerr := s.packuments.Acquire(ctx); aerr != nil {
		return nil, nil, nil, aerr //nolint:wrapcheck // caller checks for cancellation
	}

	body, err = s.readBlob(ctx, res.entry.BlobKey)
	if err != nil {
		s.packuments.Release()
		s.log.Errorw("failed to read fetched simple page", "repo", repo.Name, "path", key, "error", err)

		return nil, nil, nil, errInternal
	}

	return res, body, s.packuments.Release, nil
}

func (s *Service) readBlob(ctx context.Context, blobKey string) ([]byte, error) {
	rc, err := s.blobStore.Get(ctx, blobKey)
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}
	defer func() { base.CloseLog(rc, s.log) }()

	body, err := io.ReadAll(io.LimitReader(rc, maxPyPIPageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read blob: %w", err)
	}

	if len(body) > maxPyPIPageBytes {
		return nil, fmt.Errorf("%w: page exceeds %d bytes", errUpstream, maxPyPIPageBytes)
	}

	return body, nil
}

// servePyPIPage answers one rewritten simple page from the cached upstream
// page, answering 304 before the page is even read when the validator matches.
func (s *Service) servePyPIPage(w http.ResponseWriter, r *http.Request, repo *models.Repository, pr pageRender) {
	res, err := s.fetchPage(r.Context(), repo, pr.key, pr.target)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	h := w.Header()
	h.Add("Vary", "Accept")

	if res.stale {
		h.Set("Warning", staleWarning)
	}

	if base.Revalidate(w, r, repo.Public, pageETag(repo, pr.key, res.entry.BlobKey, pr.offer), pypiPageMaxAge) {
		return
	}

	res, body, release, err := s.readPage(r.Context(), repo, pr.key, pr.target, res)
	if err != nil {
		if r.Context().Err() == nil {
			s.failWithoutValidators(w, r, err)
		}

		return
	}

	out, err := pr.render(body, res.entry.ContentType)
	release()
	if err != nil {
		if errors.Is(err, errBadPage) {
			// Do not keep serving a body that does not parse until its TTL ends.
			s.log.Warnw("upstream simple page unparsable", "repo", repo.Name, "path", pr.key, "error", err)

			if derr := s.db.DeleteProxyCacheEntry(r.Context(), repo.ID, pr.key); derr != nil {
				s.log.Errorw("failed to drop unparsable proxy cache entry", "repo", repo.Name, "path", pr.key, "error", derr)
			}

			err = errUpstream
		} else {
			s.log.Errorw("failed to render simple page", "repo", repo.Name, "path", pr.key, "error", err)
		}

		s.failWithoutValidators(w, r, err)

		return
	}

	h.Set("Content-Type", pr.offer.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(out)
	}
}

// pypiProjectPage is a parsed upstream project page.
type pypiProjectPage struct {
	files []pypisimple.File
	// pageURL is the URL the page was fetched from; relative file links
	// resolve against it.
	pageURL *url.URL
}

// loadPyPIProject returns the parsed (cached) upstream page of project.
func (s *Service) loadPyPIProject(ctx context.Context, repo *models.Repository, project string) (*pypiProjectPage, error) {
	target, err := upstreamURL(repo.UpstreamURL, project+"/")
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return nil, errInternal
	}

	pageURL, err := url.Parse(target)
	if err != nil {
		return nil, errInternal
	}

	key := pypiProjectKey(project)

	res, err := s.fetchPage(ctx, repo, key, target)
	if err != nil {
		return nil, err
	}

	res, body, release, err := s.readPage(ctx, repo, key, target, res)
	if err != nil {
		return nil, err
	}

	files, err := pypisimple.ParseProject(body, res.entry.ContentType)
	release()
	if err != nil {
		s.log.Warnw("upstream simple page unparsable", "repo", repo.Name, "path", key, "error", err)

		if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, key); derr != nil {
			s.log.Errorw("failed to drop unparsable proxy cache entry", "repo", repo.Name, "path", key, "error", derr)
		}

		return nil, errUpstream
	}

	return &pypiProjectPage{files: files, pageURL: pageURL}, nil
}

func (s *Service) servePyPIFile(w http.ResponseWriter, r *http.Request, repo *models.Repository, project, filename string) {
	key := pypiFileKey(project, filename)

	fetch := func(ctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetchPyPIFile(ctx, repo, project, filename, key)
	}

	res, err := s.resolveWith(r.Context(), repo, key, fetch)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, func() (*resolution, error) {
		return s.resolveWith(r.Context(), repo, key, fetch)
	})
}

// fetchPyPIFile downloads a distribution file (or its PEP 658 .metadata) from
// where the cached project page says it lives, verifies it against the
// published sha256 and caches it. A file the page does not list is not found.
func (s *Service) fetchPyPIFile(ctx context.Context, repo *models.Repository, project, filename, key string) (*models.ProxyCacheEntry, error) {
	page, err := s.loadPyPIProject(ctx, repo, project)
	if err != nil {
		return nil, err
	}

	wantMeta := false
	name := filename

	if stem, ok := strings.CutSuffix(filename, pypisimple.MetadataSuffix); ok {
		name, wantMeta = stem, true
	}

	var file *pypisimple.File

	for i := range page.files {
		if page.files[i].Filename == name {
			file = &page.files[i]
			break
		}
	}

	if file == nil || (wantMeta && !file.CoreMetadata) {
		return nil, errNotFound
	}

	ref, err := url.Parse(file.URL)
	if err != nil {
		s.log.Warnw("upstream simple page has an invalid file link", "repo", repo.Name, "path", key, "error", err)
		return nil, errUpstream
	}

	target := page.pageURL.ResolveReference(ref)
	if target.Scheme != "http" && target.Scheme != "https" {
		s.log.Warnw("upstream simple page links a non-http file", "repo", repo.Name, "path", key, "scheme", target.Scheme)
		return nil, errUpstream
	}

	target.Fragment = ""
	want := file.SHA256()

	if wantMeta {
		target.Path += pypisimple.MetadataSuffix
		target.RawPath = ""
		want = file.MetadataSHA256()
	}

	return s.fetchURL(ctx, repo, key, target.String(), fetchOpts{
		noAuth:     !sameOrigin(target, page.pageURL),
		wantSHA256: want,
		unverified: want == "",
	})
}

// sameOrigin reports whether two URLs share scheme and host (with port).
// Upstream credentials are only ever sent to the upstream's own origin: an
// index may link files on a CDN.
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
