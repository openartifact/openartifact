// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/webhook"
)

// User-visible failure reasons stored on the analysis record. They are fixed
// strings: detail about the underlying error stays in the server log.
const (
	aiErrArtifactNotFound = "artifact not found in this repository"
	aiErrNoFile           = "the artifact has no stored file to read a manifest from"
	aiErrNoStore          = "blob storage is not available for manifest extraction; supply manifest_text"
	aiErrUnsupported      = "manifest extraction is not supported for this artifact; supply manifest_text"
	aiErrNoManifest       = "no manifest file was found in the artifact"
	aiErrTooLarge         = "the artifact is too large for manifest extraction"
	aiErrExtract          = "manifest extraction failed; see server logs"

	// extractTimeout bounds reading one artifact.
	extractTimeout = 2 * time.Minute

	// uploadScanLimit is how many of a repository's newest packages are
	// searched for the artifact an upload event announced.
	uploadScanLimit = 25
)

// resolve fills the request's identity (format, name, version) from the stored
// artifact. It returns the stored blob (nil when unknown) and, when the
// analysis cannot proceed, the failure reason to record. Caller-supplied
// values always win over stored ones.
func (w *AIAnalysisWorker) resolve(req AIAnalysisRequest) (AIAnalysisRequest, *models.ComponentBlob, string) {
	needIdentity := req.ArtifactFormat == "" || req.ArtifactName == "" || req.ArtifactVersion == ""
	if req.ManifestText != "" && !needIdentity {
		return req, nil, ""
	}

	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()

	blob, err := w.db.GetRepoComponentBlob(ctx, req.RepositoryID, req.ArtifactID)
	if err != nil {
		w.log.Errorw("ai worker: component lookup failed", "artifact_id", req.ArtifactID, "error", err)

		if req.ManifestText == "" {
			return req, nil, aiErrExtract
		}

		return req, nil, ""
	}

	if blob == nil {
		if req.ManifestText == "" {
			return req, nil, aiErrArtifactNotFound
		}

		return req, nil, ""
	}

	if req.ArtifactFormat == "" {
		req.ArtifactFormat = blob.Format
	}

	if req.ArtifactName == "" {
		req.ArtifactName = blob.Name
	}

	if req.ArtifactVersion == "" {
		req.ArtifactVersion = blob.Version
	}

	return req, blob, ""
}

// extract reads the manifest of the stored artifact. On failure it returns the
// reason to record on the analysis.
func (w *AIAnalysisWorker) extract(req AIAnalysisRequest, blob *models.ComponentBlob) (string, string) {
	if blob == nil {
		return "", aiErrArtifactNotFound
	}

	if !manifest.Supported(blob.Format, blob.Filename) {
		return "", aiErrUnsupported
	}

	if w.blobs == nil {
		return "", aiErrNoStore
	}

	if blob.SHA256 == "" {
		return "", aiErrNoFile
	}

	ctx, cancel := context.WithTimeout(w.ctx, extractTimeout)
	defer cancel()

	src := manifest.Source{
		Format:   blob.Format,
		Filename: blob.Filename,
		Open:     func() (io.ReadCloser, error) { return w.blobs.Get(ctx, blob.SHA256) },
	}

	if blob.Format == "conan" {
		src.Sibling = func(name string) (io.ReadCloser, error) {
			return w.conanSibling(ctx, req.RepositoryID, blob.Filename, name)
		}
	}

	text, err := manifest.Extract(ctx, src)

	switch {
	case err == nil:
		return text, ""
	case errors.Is(err, manifest.ErrUnsupported):
		return "", aiErrUnsupported
	case errors.Is(err, manifest.ErrNotFound):
		return "", aiErrNoManifest
	case errors.Is(err, manifest.ErrTooLarge):
		return "", aiErrTooLarge
	}

	w.log.Errorw("ai worker: manifest extraction failed", "artifact_id", req.ArtifactID, "format", blob.Format, "error", err)

	return "", aiErrExtract
}

// conanSibling opens a recipe file stored next to the conanmanifest.txt the
// package listing points at. reference is the listing's
// name/version@user/channel#recipe_revision; the reader is nil when the recipe
// has no such file.
func (w *AIAnalysisWorker) conanSibling(ctx context.Context, repoID, reference, file string) (io.ReadCloser, error) {
	key, ok := models.ParseConanReference(reference)
	if !ok {
		return nil, nil //nolint:nilnil // no such file
	}

	key.Filename = file

	row, err := w.db.GetConanPackage(ctx, repoID, key)
	if err != nil {
		return nil, err //nolint:wrapcheck // surfaced by the extractor
	}

	if row == nil || row.BlobKey == "" {
		return nil, nil //nolint:nilnil // no such file
	}

	return w.blobs.Get(ctx, row.BlobKey)
}

// UploadRef identifies a package an upload event announced; the worker finds
// the package's row IDs itself.
type UploadRef struct {
	RepositoryID string
	Format       string
	Name         string
	Version      string
	Path         string
	SHA256       string
}

// NotifyUpload queues a just-uploaded package for analysis when auto-analysis
// is on. It never blocks the upload: the lookup of the package's ID happens on
// the worker goroutine, and a full queue drops the notification.
func (w *AIAnalysisWorker) NotifyUpload(ref UploadRef) {
	if !w.AutoAnalyze() {
		return
	}

	select {
	case w.uploads <- ref:
	default:
		w.log.Warnw("ai analysis upload queue full, dropping notification",
			"repo_id", ref.RepositoryID, "name", ref.Name, "version", ref.Version)
	}
}

// safeResolveUpload isolates panics to a single notification.
func (w *AIAnalysisWorker) safeResolveUpload(ref UploadRef) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("ai worker: panic resolving upload", "repo_id", ref.RepositoryID, "panic", r)
		}
	}()

	w.resolveUpload(ref)
}

// resolveUpload turns an upload notification into analysis requests for the
// package files that represent it.
func (w *AIAnalysisWorker) resolveUpload(ref UploadRef) {
	if !w.AutoAnalyze() {
		return
	}

	lister, ok := w.db.(database.ComponentSearcher)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()

	comps, _, err := lister.SearchComponents(ctx, ref.RepositoryID, database.ComponentListOptions{Format: ref.Format, Page: 1, Limit: uploadScanLimit})
	if err != nil {
		w.log.Errorw("ai worker: listing packages for upload failed", "repo_id", ref.RepositoryID, "error", err)
		return
	}

	for _, c := range matchUpload(ref, comps) {
		if !manifest.AutoEligible(c.Format, c.Filename) {
			continue
		}

		w.Notify(AIAnalysisRequest{
			RepositoryID:    ref.RepositoryID,
			ArtifactID:      c.ID,
			ArtifactFormat:  c.Format,
			ArtifactName:    c.Name,
			ArtifactVersion: c.Version,
		})
	}
}

// matchUpload picks the listed packages an upload event is about, newest
// first. The strongest evidence present wins: the content digest, else the
// stored path, else name and version; a weaker rule is only consulted when a
// stronger one found nothing (an event's digest need not be the digest of the
// listed file).
func matchUpload(ref UploadRef, comps []*models.Component) []*models.Component {
	rules := []struct {
		applies bool
		match   func(*models.Component) bool
	}{
		{ref.SHA256 != "", func(c *models.Component) bool { return c.SHA256 == ref.SHA256 }},
		{ref.Path != "", func(c *models.Component) bool { return c.Filename != "" && pathMatches(ref.Path, c.Filename) }},
		{ref.Name != "", func(c *models.Component) bool { return c.Name == ref.Name && c.Version == ref.Version }},
	}

	for _, rule := range rules {
		if !rule.applies {
			continue
		}

		var out []*models.Component

		for _, c := range comps {
			if rule.match(c) {
				out = append(out, c)
			}
		}

		if len(out) > 0 {
			return out
		}
	}

	return nil
}

func pathMatches(path, filename string) bool {
	return path == filename || strings.HasSuffix(path, "/"+filename) || strings.HasSuffix(filename, "/"+path)
}

// AIAutoAnalyzePublisher is a webhook.Publisher that forwards every event to
// the wrapped publisher and, for artifact uploads, also hands the package to
// the AI worker. It is the single place uploads of every format are observed.
type AIAutoAnalyzePublisher struct {
	next   webhook.Publisher
	worker *AIAnalysisWorker
}

// NewAIAutoAnalyzePublisher wraps next (which may be nil).
func NewAIAutoAnalyzePublisher(next webhook.Publisher, w *AIAnalysisWorker) *AIAutoAnalyzePublisher {
	return &AIAutoAnalyzePublisher{next: next, worker: w}
}

// Dispatch implements webhook.Publisher.
func (p *AIAutoAnalyzePublisher) Dispatch(ctx context.Context, repoID, eventType string, payload map[string]any) {
	if eventType == webhook.EventArtifactUploaded {
		p.notify(repoID, payload)
	}

	if p.next != nil {
		p.next.Dispatch(ctx, repoID, eventType, payload)
	}
}

func (p *AIAutoAnalyzePublisher) notify(repoID string, payload map[string]any) {
	if !p.worker.AutoAnalyze() {
		return
	}

	artifact, _ := payload["artifact"].(map[string]any)
	str := func(m map[string]any, k string) string {
		v, _ := m[k].(string)
		return v
	}

	p.worker.NotifyUpload(UploadRef{
		RepositoryID: repoID,
		Format:       str(payload, "format"),
		Name:         str(artifact, "name"),
		Version:      str(artifact, "version"),
		Path:         str(artifact, "path"),
		SHA256:       str(artifact, "sha256"),
	})
}
