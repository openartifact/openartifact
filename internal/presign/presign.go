// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package presign redirects blob downloads to short-lived presigned object
// store URLs. A format handler calls TryRedirect after it has authorised the
// request and resolved the artifact, and before it opens the blob: when the
// request is a GET for a blob that is large enough, in a repository that has
// redirects enabled, on a backend that can sign URLs, the client gets a 307
// and fetches the bytes from the object store; in every other case
// TryRedirect returns false and the handler streams the blob as before.
//
// Metadata (indexes, packuments, repodata, maven-metadata, ...) is never
// presigned: handlers only call TryRedirect from their binary-download path,
// and the proxy path classifier (IsProxyBlobPath) excludes metadata files.
package presign

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Delivery outcome labels of openartifact_blob_downloads_total.
const (
	modeRedirect = "redirect"
	modeStream   = "stream"

	reasonSigned        = "presigned"
	reasonDisabled      = "disabled"
	reasonUnsupported   = "unsupported"
	reasonBelowSize     = "below_min_size"
	reasonSignFailed    = "sign_failed"
	reasonRepoLookup    = "repo_lookup_failed"
	reasonMissingObject = "missing_object"
)

// Settings is the global presign configuration (storage.presign*).
type Settings struct {
	// Downloads enables redirects for every repository without an override.
	Downloads bool
	// MinSize is the smallest blob (bytes) that is redirected.
	MinSize int64
	// TTL is the lifetime of a presigned download URL.
	TTL time.Duration
}

// SettingsFromConfig derives Settings from the storage configuration.
func SettingsFromConfig(cfg storage.StorageConfig) Settings {
	return Settings{Downloads: cfg.PresignDownloads, MinSize: cfg.PresignMinSize, TTL: cfg.PresignTTL}
}

// Blob describes the blob a handler is about to serve.
type Blob struct {
	// SHA256 is the blob's content address (the blob store key).
	SHA256 string
	// Size is the blob size in bytes; <= 0 asks the store.
	Size int64
	// Filename, when set, becomes the Content-Disposition filename of the
	// presigned response so clients save the file under the right name.
	Filename string
	// ContentType, when set, overrides the stored object's Content-Type.
	ContentType string
	// Repo is the owning repository's name (for the per-repository override).
	Repo string
	// Repository is the owning repository when the caller already holds it;
	// it takes precedence over Repo and saves the lookup.
	Repository *models.Repository
	// CheckExists makes TryRedirect verify the object is present before
	// redirecting, for callers (proxy cache) whose index can outlive the blob.
	CheckExists bool
}

// Redirector answers a blob download with a redirect when it can.
type Redirector interface {
	// TryRedirect writes a 307 to a presigned URL and returns true, or
	// returns false having written nothing (the caller streams the blob).
	TryRedirect(w http.ResponseWriter, r *http.Request, blob Blob) bool
}

// Try calls TryRedirect when store is a redirect-capable *Store (or anything
// else implementing Redirector) and reports whether a redirect was written.
// Stores without the capability never redirect.
func Try(store storage.BlobStore, w http.ResponseWriter, r *http.Request, blob Blob) bool {
	rd, ok := store.(Redirector)

	return ok && rd.TryRedirect(w, r, blob)
}

// Store is a blob store that can redirect downloads. It is installed in place
// of the plain store handed to format handlers, which discover the capability
// through the Redirector interface; everything else sees an ordinary BlobStore.
type Store struct {
	storage.BlobStore

	signer    storage.Presigner
	db        database.Database
	settings  Settings
	reg       *metrics.Registry
	log       *logger.Logger
	supported bool
}

var _ Redirector = (*Store)(nil)

// Wrap decorates inner with redirect support. Whether inner can sign URLs is
// probed once, here, without any network traffic.
func Wrap(inner storage.BlobStore, db database.Database, settings Settings, reg *metrics.Registry, log *logger.Logger) *Store {
	s := &Store{BlobStore: inner, db: db, settings: settings, reg: reg, log: log}

	if signer, ok := inner.(storage.Presigner); ok {
		// A syntactically valid id; signing is a local computation.
		_, err := signer.PresignGet(context.Background(), probeID, time.Minute, storage.PresignGetOptions{})
		s.signer, s.supported = signer, err == nil
	}

	return s
}

// Supported reports whether the backend can sign download URLs.
func (s *Store) Supported() bool { return s.supported }

// Settings returns the global configuration.
func (s *Store) Settings() Settings { return s.settings }

const probeID = "0000000000000000000000000000000000000000000000000000000000000000"

// TryRedirect implements Redirector.
func (s *Store) TryRedirect(w http.ResponseWriter, r *http.Request, blob Blob) bool {
	if r.Method != http.MethodGet {
		// HEAD (and anything else) is always answered locally.
		return false
	}

	reason, ok := s.redirect(w, r, blob)
	if ok {
		s.count(modeRedirect, reasonSigned)
		return true
	}

	s.count(modeStream, reason)

	return false
}

func (s *Store) redirect(w http.ResponseWriter, r *http.Request, blob Blob) (string, bool) {
	enabled, reason := s.enabledFor(r.Context(), blob)
	if !enabled {
		return reason, false
	}

	if !s.supported {
		return reasonUnsupported, false
	}

	if blob.Size <= 0 {
		size, err := s.GetSize(r.Context(), blob.SHA256)
		if err != nil {
			return reasonMissingObject, false
		}

		blob.Size = size
	}

	if blob.Size < s.settings.MinSize {
		return reasonBelowSize, false
	}

	if blob.CheckExists {
		if exists, err := s.Exists(r.Context(), blob.SHA256); err != nil || !exists {
			return reasonMissingObject, false
		}
	}

	target, err := s.signer.PresignGet(r.Context(), blob.SHA256, s.settings.TTL, storage.PresignGetOptions{
		ContentDisposition: contentDisposition(blob.Filename),
		ContentType:        blob.ContentType,
	})
	if err != nil {
		if !errors.Is(err, storage.ErrPresignNotSupported) {
			// Never log the URL; the error carries none, only the blob id.
			s.log.Warnw("presign failed, streaming instead", "sha256", blob.SHA256, "error", err)
		}

		return reasonSignFailed, false
	}

	h := w.Header()
	h.Set("Location", target)
	// The URL is a bearer credential that expires: never let a cache keep it.
	h.Set("Cache-Control", "private, no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Length", strconv.Itoa(0))
	w.WriteHeader(http.StatusTemporaryRedirect)

	return "", true
}

// enabledFor resolves the effective setting: the repository override when
// set, else the global switch. The second result is the metrics reason for a
// negative answer.
func (s *Store) enabledFor(ctx context.Context, blob Blob) (bool, string) {
	repo := blob.Repository

	if repo == nil && blob.Repo != "" && s.db != nil {
		var err error

		repo, err = s.db.GetRepositoryByName(ctx, blob.Repo)
		if err != nil {
			s.log.Warnw("presign: repository lookup failed, streaming instead", "repo", blob.Repo, "error", err)
			return false, reasonRepoLookup
		}
	}

	if repo != nil && repo.PresignDownloads != nil {
		return *repo.PresignDownloads, reasonDisabled
	}

	return s.settings.Downloads, reasonDisabled
}

func (s *Store) count(mode, reason string) {
	if s.reg != nil {
		s.reg.BlobDownloadsTotal.WithLabelValues(mode, reason).Inc()
	}
}

// contentDisposition renders an attachment Content-Disposition for filename,
// or "" when there is none or it cannot be encoded.
func contentDisposition(filename string) string {
	if filename == "" {
		return ""
	}

	return mime.FormatMediaType("attachment", map[string]string{"filename": filename})
}

// HealthCheck probes the store this one wraps.
func (s *Store) HealthCheck(ctx context.Context) error {
	return storage.CheckHealth(ctx, s.BlobStore)
}
