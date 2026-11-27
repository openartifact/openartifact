// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"io"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/pkg/p2"
)

// P2Hooks is the proxy hooks of the p2 format: an Eclipse p2 update site is a
// tree of static files, so the generic path-mirroring proxy serves it. What the
// generic proxy cannot do is keep a client on the proxy: the repository
// documents name mirrors, a statistics endpoint and composite children that
// point at other places. Fetch rewrites those documents before they are
// cached (pkg/p2), and serves the upstream host's other paths under "_host/".
var P2Hooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Fetch: (*Service).fetchP2,
	// The bundles and features of an update site are named by id and version.
	Immutable:   p2.IsArtifact,
	IsMetadata:  p2.IsMetadata,
	PackageExts: []string{".jar", ".gz", ".xz", ".xml", ".index"},
	Coordinates: p2.Coordinates,
	BlobPath:    p2.IsArtifact,
}

// fetchP2 downloads key, a path below the update site or below its host
// ("_host/..."), and rewrites it when it is a repository document. target is
// ignored: the generic target does not know the "_host/" namespace.
func (s *Service) fetchP2(
	ctx context.Context, repo *models.Repository, key, _ string,
) (*models.ProxyCacheEntry, error) {
	target, ok := p2.UpstreamURL(repo.UpstreamURL, key)
	if !ok {
		return nil, errNotFound
	}

	var o fetchOpts

	if doc, isDoc := p2.ParseDoc(key); isDoc {
		o.transform = func(resp *http.Response) error { return s.rewriteP2(repo, key, target, doc, resp) }
	}

	return s.fetchURL(ctx, repo, key, target, o)
}

// rewriteP2 replaces resp.Body, the upstream copy of the repository document
// doc, by its rewritten form in a spool file removed when fetchURL closes it.
// The upstream body is spooled first, under the metadata limit, so that a
// network failure is an upstream failure and a jar can be read at random. A
// document that does not rewrite (not XML, a bomb, a directive) is refused like
// any upstream body that fails verification.
func (s *Service) rewriteP2(repo *models.Repository, key, target string, doc p2.Doc, resp *http.Response) error {
	rel, err := p2.NewRelocator(repo.UpstreamURL, target, key)
	if err != nil {
		s.log.Errorw("p2 upstream URL unusable", "repo", repo.Name, "path", key, "error", err)
		return errInternal
	}

	in, size, err := s.spoolUpstreamBody(repo, key, resp, s.maxMeta)
	if err != nil {
		return err
	}
	defer func() { base.CloseLog(in, s.log) }()

	out, err := s.newSpool()
	if err != nil {
		return err
	}

	if rerr := doc.Rewrite(out, in, size, p2.Options{Relocate: rel.Relocate}); rerr != nil {
		base.CloseLog(out, s.log)
		s.log.Warnw("proxy upstream p2 document refused", "repo", repo.Name, "path", key, "error", rerr)

		return errVerify
	}

	outSize, err := out.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = out.Seek(0, io.SeekStart)
	}

	if err != nil {
		base.CloseLog(out, s.log)
		s.log.Errorw("failed to rewind proxy spool file", "error", err)

		return errInternal
	}

	resp.Body = out
	resp.ContentLength = outSize
	resp.TransferEncoding = nil

	return nil
}
