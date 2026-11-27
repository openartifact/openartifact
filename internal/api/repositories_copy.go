// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/lifecycle"
	"github.com/openartifact/openartifact/internal/license"
	"github.com/openartifact/openartifact/internal/promote"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Copy and move of artifacts between hosted repositories, built on the
// promotion engine (internal/promote): POST /api/v1/repositories/{name}/copy and
// .../move, with {name} the source.
//
// Like a bundle promotion, the routes are not `manage`: the RBAC middleware only
// requires a real principal (middleware.isRepoTransferRoute) and the handler
// authorizes every repository itself. A copy needs `read` on the source at each
// artifact's path and `write` on the target; a move also `delete` on the source
// at each artifact's path. Project roles apply through each repository's
// project, as for every repository permission. `force` is for administrators.

// maxCopyItems caps the items of one copy or move request.
const maxCopyItems = 500

// CopyMoveStore is the part of the database CopyMoveHandler needs.
type CopyMoveStore interface {
	promote.EngineStore
	repoAccessStore
}

// CopyMoveHandler serves the copy and move routes.
type CopyMoveHandler struct {
	repoAccess

	db        CopyMoveStore
	engine    *promote.Engine
	publisher webhook.Publisher
	workers   *lifecycle.Workers
	licenses  promote.LicenseSource
}

// NewCopyMoveHandler returns the handler of the copy and move routes.
func NewCopyMoveHandler(db CopyMoveStore, log *logger.Logger, evaluator *auth.Evaluator) *CopyMoveHandler {
	return &CopyMoveHandler{repoAccess: newRepoAccess(db, log, evaluator), db: db, engine: promote.NewEngine(db, log, nil, nil)}
}

// WithPublisher enables the artifact.uploaded and artifact.deleted webhook events.
func (h *CopyMoveHandler) WithPublisher(p webhook.Publisher) *CopyMoveHandler {
	h.publisher = p
	return h
}

// WithWorkers enqueues the refresh of generated metadata in the target (and, for
// a move, the source) repository.
func (h *CopyMoveHandler) WithWorkers(w *lifecycle.Workers) *CopyMoveHandler {
	h.workers = w
	h.engine = promote.NewEngine(h.db, h.log, h.workers, h.licenses)

	return h
}

// WithLicenseFiles enables the license policy of the target for the formats that
// keep their license in the package file alone, as for a bundle promotion.
func (h *CopyMoveHandler) WithLicenseFiles(db LicenseStore, blobs storage.BlobStore) *CopyMoveHandler {
	h.licenses = promoteLicenses{src: artifactLicenseSource{db: db, blobs: blobs, log: h.log, detector: license.NewDetector(h.log)}}
	h.engine = promote.NewEngine(h.db, h.log, h.workers, h.licenses)

	return h
}

// RegisterAuthed registers the routes.
func (h *CopyMoveHandler) RegisterAuthed(r chi.Router) {
	r.Post("/api/v1/repositories/{name}/copy", func(w http.ResponseWriter, r *http.Request) { h.handle(w, r, false) })
	r.Post("/api/v1/repositories/{name}/move", func(w http.ResponseWriter, r *http.Request) { h.handle(w, r, true) })
}

// copyItemInput is one item of the request: a package version, or a content
// digest with an optional path (for docker the manifest digest with an optional tag).
type copyItemInput struct {
	Package string `json:"package"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Path    string `json:"path"`
	Tag     string `json:"tag"`
}

type copyRequest struct {
	Target string          `json:"target"`
	Items  []copyItemInput `json:"items"`
	DryRun bool            `json:"dry_run"`
	Force  bool            `json:"force"`
}

// items converts the request items. An item that names neither a package
// version nor a digest is left for the engine to refuse.
func (b copyRequest) items() []promote.CopyItem {
	out := make([]promote.CopyItem, len(b.Items))

	for i, it := range b.Items {
		path := it.Path
		if path == "" {
			path = it.Tag
		}

		out[i] = promote.CopyItem{
			Package: it.Package, Version: it.Version,
			SHA256: strings.TrimPrefix(strings.ToLower(it.SHA256), "sha256:"), Path: path,
		}
	}

	return out
}

func (h *CopyMoveHandler) handle(w http.ResponseWriter, r *http.Request, move bool) {
	ctx := r.Context()
	p := currentPrincipal(r)

	if p == nil || p.IsAnonymous {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	src, ok := h.repoNamed(w, r, "copy: db error getting source repo")
	if !ok {
		return
	}

	admin, err := principalIsAdmin(ctx, p, h.evaluator)
	if err != nil {
		h.authError(w, err)
		return
	}

	if !admin && !h.visible(w, ctx, p, src, "repository not found") {
		return
	}

	var body copyRequest
	if err := decodeJSON(r, &body); err != nil {
		writeBadJSON(w, err)
		return
	}

	if body.Target == "" || len(body.Items) == 0 || len(body.Items) > maxCopyItems {
		writeError(w, http.StatusBadRequest, "validation_failed",
			"target is required and items must contain between 1 and "+strconv.Itoa(maxCopyItems)+" entries")

		return
	}

	if body.Force && !admin {
		writeError(w, http.StatusForbidden, "forbidden", "force requires an administrator")
		return
	}

	dst, err := h.db.GetRepositoryByName(ctx, body.Target)
	if err != nil {
		h.dbError(w, "copy: db error getting target repo", err)
		return
	}

	if dst == nil {
		writeError(w, http.StatusNotFound, "not_found", "target repository not found")
		return
	}

	if !admin && !h.canWriteTarget(w, ctx, p, dst) {
		return
	}

	report, err := h.engine.Copy(ctx, promote.CopyParams{
		Source: src, Target: dst, Items: body.items(), Move: move, DryRun: body.DryRun, Force: body.Force,
		Actor: p.Username, Authorize: h.authorizer(p, admin, src, move),
	})
	if err != nil {
		h.writeError(w, move, err)
		return
	}

	if !body.DryRun {
		h.announce(r, src, dst, report, move)
	}

	writeJSON(w, http.StatusOK, copyResponse(src, dst, report, move, body.DryRun))
}

// authError answers a failed authorization lookup.
func (h *CopyMoveHandler) authError(w http.ResponseWriter, err error) {
	h.log.Errorw("copy: auth evaluation error", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "auth evaluation failed")
}

// visible answers 404 msg unless the principal can read repo at all (a repository
// the caller cannot see does not exist for them).
func (h *CopyMoveHandler) visible(w http.ResponseWriter, ctx context.Context, p *auth.Principal, repo *models.Repository, msg string) bool {
	canRead, err := h.evaluator.RepoReadFilter(ctx, *p)
	if err != nil {
		h.authError(w, err)
		return false
	}

	ok, err := canRead(repo)
	if err != nil {
		h.authError(w, err)
		return false
	}

	if !ok {
		writeError(w, http.StatusNotFound, "not_found", msg)
	}

	return ok
}

// canWriteTarget checks `write` on the target; a caller who cannot even see it
// gets 404, like an unknown repository.
func (h *CopyMoveHandler) canWriteTarget(w http.ResponseWriter, ctx context.Context, p *auth.Principal, dst *models.Repository) bool {
	canWrite, err := h.evaluator.Evaluate(ctx, *p, dst.Name, "", "write")
	if err != nil {
		h.authError(w, err)
		return false
	}

	if canWrite {
		return true
	}

	if !h.visible(w, ctx, p, dst, "target repository not found") {
		return false
	}

	writeError(w, http.StatusForbidden, "forbidden", "write access to the target repository is required")

	return false
}

// authorizer returns the per-artifact check of the engine: `read` (and for a
// move `delete`) on the source at the artifact's path.
func (h *CopyMoveHandler) authorizer(
	p *auth.Principal,
	admin bool,
	src *models.Repository,
	move bool,
) func(context.Context, promote.Artifact) error {
	return func(ctx context.Context, a promote.Artifact) error {
		if admin {
			return nil
		}

		actions := []string{"read"}
		if move {
			actions = append(actions, "delete")
		}

		for _, action := range actions {
			ok, err := h.evaluator.Evaluate(ctx, *p, src.Name, a.Path, action)
			if err != nil {
				return fmt.Errorf("evaluate %s on %s: %w", action, a.Path, err)
			}

			if !ok {
				return fmt.Errorf("%w: %s on %s", promote.ErrNotPermitted, action, a.Path)
			}
		}

		return nil
	}
}

// writeError maps a failed copy or move to its HTTP answer. The database is
// unchanged whenever it fires: a copy is one transaction.
func (h *CopyMoveHandler) writeError(w http.ResponseWriter, move bool, err error) {
	which := promoteItemLabelOf(err)

	switch {
	case writePromoteFailure(w, err, which, "the target repository changed concurrently; retry"):
	case errors.Is(err, promote.ErrNotPermitted):
		writeError(w, http.StatusForbidden, "forbidden", "you may not "+verb(move)+" this artifact"+which)
	case errors.Is(err, promote.ErrItemNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such artifact in the source repository"+which)
	case errors.Is(err, promote.ErrSourceImmutable):
		writeError(w, http.StatusConflict, "immutable_release", "the source repository has immutable releases"+which)
	case errors.Is(err, promote.ErrSourceHeld):
		writeError(w, http.StatusConflict, "held_by_bundle", err.Error()+which)
	case errors.Is(err, promote.ErrNoItems), errors.Is(err, promote.ErrBadItem):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error()+which)
	case errors.Is(err, promote.ErrFormatMismatch), errors.Is(err, promote.ErrNotHosted),
		errors.Is(err, promote.ErrSameRepository), errors.Is(err, promote.ErrUnsupportedFormat):
		writeError(w, http.StatusBadRequest, "validation_failed", copyInvalidMessage(err)+which)
	default:
		h.log.Errorw("copy failed", "move", move, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", verb(move)+" failed")
	}
}

// copyInvalidMessage explains a request for repositories a copy cannot connect.
func copyInvalidMessage(err error) string {
	switch {
	case errors.Is(err, promote.ErrFormatMismatch):
		return "the source and the target repository have different formats"
	case errors.Is(err, promote.ErrSameRepository):
		return "the target repository is the source repository"
	case errors.Is(err, promote.ErrUnsupportedFormat):
		return "copying is not supported for this format"
	default:
		return "copy and move need hosted source and target repositories"
	}
}

func verb(move bool) string {
	if move {
		return "move"
	}

	return "copy"
}

// copyResponse renders the report for the API.
func copyResponse(src, dst *models.Repository, rep *promote.CopyReport, move, dryRun bool) map[string]any {
	out := map[string]any{
		"operation": verb(move),
		"source":    src.Name,
		"target":    dst.Name,
		"dry_run":   dryRun,
		"copied":    rep.Copied,
		"existing":  rep.Existing,
		"replaced":  rep.Replaced,
		"items":     rep.Items,
	}

	if move {
		out["moved"] = len(rep.Moved)
	}

	return out
}

// announce emits the audit events and webhooks of a committed copy or move.
func (h *CopyMoveHandler) announce(r *http.Request, src, dst *models.Repository, rep *promote.CopyReport, move bool) {
	action := audit.ActionArtifactCopied
	if move {
		action = audit.ActionArtifactMoved
	}

	logAdminAudit(r, adminAuditEvent{
		repositoryID: dst.ID, action: action, resourceType: "repository", resourceID: src.ID,
		details: fmt.Sprintf("%s from %s to %s: %d copied, %d already present, %d replaced (%s)",
			verb(move), src.Name, dst.Name, rep.Copied, rep.Existing, rep.Replaced, itemSummary(rep)),
		subject: src.Name + " -> " + dst.Name,
	})

	for _, a := range rep.Uploaded {
		logAdminAudit(r, adminAuditEvent{
			repositoryID: dst.ID, action: audit.ActionArtifactUploaded, resourceType: "repository", resourceID: dst.ID,
			details: verb(move) + "d from " + src.Name + ": " + a.Path, pkg: a.Name, version: a.Version, subject: a.Path,
		})

		webhook.Emit(r.Context(), h.publisher, dst.ID, webhook.EventArtifactUploaded,
			webhook.ArtifactPayload(webhook.EventArtifactUploaded, webhookArtifact(dst, a)))
	}

	if !move {
		return
	}

	for _, a := range rep.Moved {
		logAdminAudit(r, adminAuditEvent{
			repositoryID: src.ID, action: audit.ActionArtifactDeleted, resourceType: "repository", resourceID: src.ID,
			details: "moved to trash: moved to " + dst.Name + ": " + a.Path, pkg: a.Name, version: a.Version, subject: a.Path,
		})

		webhook.Emit(r.Context(), h.publisher, src.ID, webhook.EventArtifactDeleted,
			webhook.ArtifactPayload(webhook.EventArtifactDeleted, webhookArtifact(src, a)))
	}
}

func webhookArtifact(repo *models.Repository, a promote.Artifact) webhook.Artifact {
	return webhook.Artifact{
		Repository: repo.Name, Format: repo.Format, Name: a.Name, Version: a.Version, Path: a.Path, SHA256: a.SHA256, Size: a.Size,
	}
}

// itemSummary lists the first requested items for the audit details.
func itemSummary(rep *promote.CopyReport) string {
	const shown = 10

	var parts []string

	for i, it := range rep.Items {
		if i == shown {
			parts = append(parts, fmt.Sprintf("and %d more", len(rep.Items)-shown))
			break
		}

		switch {
		case it.Package != "":
			parts = append(parts, it.Package+"@"+it.Version)
		case it.ArtifactPath != "":
			parts = append(parts, it.ArtifactPath)
		default:
			parts = append(parts, "sha256:"+it.SHA256)
		}
	}

	return strings.Join(parts, ", ")
}
