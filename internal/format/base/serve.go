// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ServeRendered revalidates a stamp-validated metadata document and serves it:
// a matching If-None-Match is answered 304 from etag alone, otherwise the
// document is rendered once per etag-derived key (see Handler.Rendered) and
// written. failMsg and fields describe the document in the error log written
// when rendering fails (the error itself is appended), and the client gets 500.
func (h *Handler) ServeRendered(
	w http.ResponseWriter,
	r *http.Request,
	repoPublic bool,
	etag, key string,
	maxAge int,
	render func(ctx context.Context) (*Rendered, error),
	failMsg string,
	fields ...any,
) {
	if Revalidate(w, r, repoPublic, etag, maxAge) {
		return
	}

	rendered, err := h.Rendered(r.Context(), key, render)
	if err != nil {
		h.Log.Errorw(failMsg, append(fields, "error", err)...)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	rendered.Serve(w, r)
}

// BlobOptions shapes the response of ServeRepoMetadata.
type BlobOptions struct {
	ContentType string

	// MaxAge is the shared-cache lifetime of a public repository's response.
	MaxAge int

	// ContentLength sends the blob size as Content-Length.
	ContentLength bool

	// LastModified sends the generation time as Last-Modified and answers an
	// If-Modified-Since that is not older with 304 when the request carries no
	// If-None-Match (what pacman asks).
	LastModified bool
}

// ServeRepoMetadataStore is the part of the database ServeRepoMetadata uses needs.
type ServeRepoMetadataStore interface {
	GetRepoMetadataByPath(ctx context.Context, repositoryID string, path string) (*models.RepoMetadata, error)
}

// ServeRepoMetadata serves the pre-generated metadata blob stored under path.
// The blob digest is the ETag, so a matching If-None-Match is answered without
// reading the blob. It returns true when a response was written (the blob, a
// 304 or a 500) and false, with nothing written, when no blob exists at path.
func ServeRepoMetadata(
	w http.ResponseWriter,
	r *http.Request,
	log *logger.Logger,
	db ServeRepoMetadataStore,
	store storage.BlobStore,
	repo *models.Repository,
	path string,
	opts BlobOptions,
) bool {
	meta, err := db.GetRepoMetadataByPath(r.Context(), repo.ID, path)
	if err != nil {
		log.Errorw("failed to query repo metadata", "path", path, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return true
	}

	if meta == nil {
		return false
	}

	if opts.LastModified {
		w.Header().Set("Last-Modified", meta.LastGenerated.UTC().Format(http.TimeFormat))

		if r.Header.Get("If-None-Match") == "" && notModifiedSince(r, meta.LastGenerated) {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}

	if Revalidate(w, r, repo.Public, `"`+meta.BlobSHA256+`"`, opts.MaxAge) {
		return true
	}

	reader, err := store.Get(r.Context(), meta.BlobSHA256)
	if err != nil {
		log.Errorw("failed to read blob", "sha256", meta.BlobSHA256, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return true
	}
	defer CloseLog(reader, log)

	w.Header().Set("Content-Type", opts.ContentType)

	if opts.ContentLength {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.BlobSize, 10))
		w.WriteHeader(http.StatusOK)
	}

	if r.Method == http.MethodHead {
		return true
	}

	if _, err := iocopy.Copy(w, reader); err != nil {
		log.Warnw("failed to write response", "path", path, "error", err)
	}

	return true
}

// notModifiedSince reports whether the client's If-Modified-Since is not older
// than modified (HTTP dates have one-second resolution).
func notModifiedSince(r *http.Request, modified time.Time) bool {
	since, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
	if err != nil {
		return false
	}

	return !modified.Truncate(time.Second).After(since)
}
