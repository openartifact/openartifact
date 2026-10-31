// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
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
// repository (links embed its name), the path, the serialization and the
// version gate (Gate.Stamp, "" for a page the gate does not apply to).
func pageETag(repo *models.Repository, key, blobKey string, offer pypisimple.Offer, gate string) string {
	in := repo.Name + "\x00" + key + "\x00" + blobKey + "\x00" + offer.ContentType
	if gate != "" {
		in += "\x00" + gate
	}

	sum := sha256.Sum256([]byte(in))

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

	s.servePyPIPage(w, r, repo, offer, pypiPageDoc(pypiIndexKey, ""), "", func(body []byte, contentType string) ([]byte, error) {
		names, perr := pypisimple.ParseIndex(body, contentType)
		if perr != nil {
			return nil, fmt.Errorf("%w: %w", errUnusableDoc, perr)
		}

		return pypisimple.RenderIndex(offer.Format, names, pypisimple.ProjectURL)
	})
}

func (s *Service) servePyPIProject(w http.ResponseWriter, r *http.Request, repo *models.Repository, project string) {
	offer, ok := pypisimple.Negotiate(r.Header.Get("Accept"))
	if !ok {
		http.Error(w, "no acceptable simple API serialization", http.StatusNotAcceptable)
		return
	}

	// One clock reading per request: the validator and the rendering agree on
	// which versions are old enough.
	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.servePyPIPage(
		w,
		r,
		repo,
		offer,
		pypiPageDoc(pypiProjectKey(project), project+"/"),
		g.Stamp(),
		func(body []byte, contentType string) ([]byte, error) {
			files, perr := pypisimple.ParseProject(body, contentType)
			if perr != nil {
				return nil, fmt.Errorf("%w: %w", errUnusableDoc, perr)
			}

			files, perr = gatePyPIFiles(g, project, files)
			if perr != nil {
				return nil, perr
			}

			return pypisimple.RenderProject(offer.Format, project, pypisimple.ServableFiles(files), func(f *pypisimple.File) string {
				return pypisimple.FileURL(project, f.Filename)
			})
		},
	)
}

// gatePyPIFiles applies the version gate to a project page's files: the files
// of a release the gate holds back are dropped. Curation judges each file by the
// version its name spells (SpelledVersion); the minimum age by the first upload
// of the release (PEP 700 upload-time, JSON pages only; a release no file of
// which states a time passes). A page that listed files and has none left is a
// project with nothing to offer: errNotFound, as pypi answers for an unknown
// project. Only the files are rendered, so a PEP 700 "versions" list of the
// upstream never reaches the client.
func gatePyPIFiles(g versionGate, project string, files []pypisimple.File) ([]pypisimple.File, error) {
	if !g.Active() || len(files) == 0 {
		return files, nil
	}

	kept := pypisimple.NewReleases(project, files).Filter(project, files, func(version string, published time.Time) bool {
		hidden, _ := g.Hidden(project, version, published)
		return !hidden
	})
	if len(kept) == 0 {
		return nil, errNotFound
	}

	return kept, nil
}

// servePyPIPage answers one rewritten simple page from the cached upstream
// page in the negotiated serialization.
func (s *Service) servePyPIPage(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	offer pypisimple.Offer,
	doc metaDoc,
	gate string,
	render func(body []byte, contentType string) ([]byte, error),
) {
	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: doc,
		maxAge:  pypiPageMaxAge,
		vary:    "Accept",
		etag:    func(blobKey string) string { return pageETag(repo, doc.key, blobKey, offer, gate) },
		render: func(body []byte, res *resolution) ([]byte, string, error) {
			out, err := render(body, res.entry.ContentType)
			return out, offer.ContentType, err
		},
	})
}

func (s *Service) servePyPIFile(w http.ResponseWriter, r *http.Request, repo *models.Repository, project, filename string) {
	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	// A file already in the cache is served without locate, and with it the
	// minimum age (a file that was served once stays served). Curation is
	// checked first anyway, from the file name alone, so a version curated after
	// it was cached is refused too.
	if g.Set.Active() {
		if gerr := checkGate(g, project, pypisimple.SpelledVersion(project, filename), time.Time{}); gerr != nil {
			s.writeResolveError(w, r, gerr)
			return
		}
	}

	s.serveDownload(w, r, repo, pypiFileKey(project, filename), func(ctx context.Context) (download, error) {
		return s.locatePyPIFile(ctx, repo, project, filename, g)
	})
}

// locatePyPIFile finds a distribution file (or its PEP 658 .metadata) where
// the cached project page says it lives, with the published sha256 it must
// have. A file the page does not list is not found.
func (s *Service) locatePyPIFile(ctx context.Context, repo *models.Repository, project, filename string, g versionGate) (download, error) {
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

	published := pypisimple.NewReleases(project, files).Published(project, name)
	if gerr := checkGate(g, project, pypisimple.SpelledVersion(project, name), published); gerr != nil {
		return download{}, gerr
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

// PyPIHooks is the proxy hooks of the pypi format.
var PyPIHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).servePyPI,
	// Distribution files (and their PEP 658 metadata), not the index pages.
	Immutable:  func(key string) bool { return strings.HasPrefix(key, "files/") },
	IsMetadata: func(key string) bool { return !strings.HasPrefix(key, "files/") },
	BlobPath:   presign.PyPIBlobPath,
	Published:  (*Service).pypiPublished,
	Versions:   (*Service).pypiVersions,
}

// pypiPublished dates version of project (a release, in the spelling of its file
// names or any equivalent one) from the cached project page: the first upload of
// the release (PEP 700, JSON pages only).
func (s *Service) pypiPublished(ctx context.Context, repo *models.Repository, project, version string) (time.Time, bool) {
	project = pypisimple.NormalizeName(project)

	entry, err := s.db.GetProxyCacheEntry(ctx, repo.ID, pypiProjectKey(project))
	if err != nil || entry == nil || !pypisimple.IsJSON(entry.ContentType) {
		return time.Time{}, false // HTML pages state no upload times
	}

	body, ok := s.cachedDocBody(ctx, repo, pypiPageDoc(pypiProjectKey(project), project+"/"))
	if !ok {
		return time.Time{}, false
	}

	files, err := pypisimple.ParseProject(body, entry.ContentType)
	if err != nil {
		return time.Time{}, false
	}

	published := pypisimple.NewReleases(project, files).PublishedVersion(version)

	return published, !published.IsZero()
}
