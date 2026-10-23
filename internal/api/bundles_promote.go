// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/promote"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/trash"
	"github.com/openartifact/openartifact/internal/webhook"
)

// promoteBundle copies the sealed bundle's items into targetRepo and moves the
// bundle to toStage. The request is already authorized and the stage
// transition validated; the promote engine does the copy atomically.
func (h *BundlesHandler) promoteBundle(w http.ResponseWriter, r *http.Request, bundle *models.ReleaseBundle, targetRepo *models.Repository, toStage, notes string) {
	ctx := r.Context()

	items, err := h.db.ListBundleItems(ctx, bundle.ID)
	if err != nil {
		h.log.Errorw("promote bundle: db error listing items", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "database error")

		return
	}

	var promoter string
	if p := currentPrincipal(r); p != nil {
		promoter = p.Username
	}

	report, err := h.promoter.Promote(ctx, promote.Params{
		Bundle:   bundle,
		Items:    items,
		Target:   targetRepo,
		ToStage:  toStage,
		Notes:    notes,
		Promoter: promoter,
		Force:    trash.ForceRequested(r) && trash.IsAdmin(ctx),
	})
	if err != nil {
		h.writePromoteError(w, bundle, err)
		return
	}

	h.announcePromotion(r, bundle, targetRepo, report)

	writeJSON(w, http.StatusOK, promoteResponse(report))
}

// promoteResponse renders a promotion report for the API.
func promoteResponse(report *promote.Report) map[string]any {
	return map[string]any{
		"promotion": promotionToMap(report.Promotion),
		"copied":    report.Copied,
		"existing":  report.Existing,
		"replaced":  report.Replaced,
		"items":     report.Items,
	}
}

// announcePromotion audits a committed promotion and emits bundle.promoted.
func (h *BundlesHandler) announcePromotion(r *http.Request, bundle *models.ReleaseBundle, target *models.Repository, report *promote.Report) {
	from := ""
	if report.Promotion.FromStage != nil {
		from = *report.Promotion.FromStage
	}

	details := fmt.Sprintf("bundle %s %s promoted to stage %s (%s): %d items, %d artifacts copied, %d already present, %d replaced",
		bundle.Name, bundle.Version, report.Promotion.ToStage, target.Name, report.Promotion.ItemCount, report.Copied, report.Existing, report.Replaced)
	auditPromotion(r, bundle.ID, target.ID, details)

	webhook.Emit(r.Context(), h.publisher, target.ID, webhook.EventBundlePromoted, webhook.BundlePromotedPayload(webhook.BundlePromotion{
		Repository: target.Name,
		Format:     target.Format,
		Bundle:     bundle.Name,
		Version:    bundle.Version,
		FromStage:  from,
		ToStage:    report.Promotion.ToStage,
		Items:      report.Promotion.ItemCount,
		Copied:     report.Copied,
		Existing:   report.Existing,
		Replaced:   report.Replaced,
	}))
}

// auditPromotion records the bundle.promoted audit event against the target repository.
func auditPromotion(r *http.Request, bundleID, repoID, details string) {
	l := audit.Default()
	if l == nil {
		return
	}

	resourceType := "bundle"
	ev := audit.AuditEvent{
		Action:       audit.ActionBundlePromoted,
		ActorType:    "system",
		ResourceType: &resourceType,
		ResourceID:   &bundleID,
		RepositoryID: &repoID,
		Details:      details,
	}

	if p := currentPrincipal(r); p != nil && !p.IsAnonymous {
		ev.ActorType = "user"
		ev.Actor = p.Username
		ev.ActorID = p.UserID

		if p.IsToken() {
			ev.ActorType = "token"
		}
	}

	l.Log(r.Context(), ev)
}

// writePromoteError maps a promotion failure to its HTTP answer. The database
// is unchanged whenever it fires: the promotion is one transaction.
func (h *BundlesHandler) writePromoteError(w http.ResponseWriter, bundle *models.ReleaseBundle, err error) {
	var (
		item     *promote.ItemError
		conflict *promote.ConflictError
		blocked  *promote.LicenseBlockedError
	)

	which := ""
	if errors.As(err, &item) {
		which = promoteItemLabel(item)
	}

	switch {
	case errors.As(err, &conflict):
		code := "artifact_conflict"
		if conflict.Immutable {
			code = "immutable_release"
		}

		writeError(w, http.StatusConflict, code, conflict.Error()+which)
	case errors.Is(err, quota.ErrExceeded):
		quota.WriteExceeded(w)
	case errors.Is(err, promote.ErrQuarantined):
		writeError(w, http.StatusForbidden, "ARTIFACT_QUARANTINED", "an item is quarantined in its source repository"+which)
	case errors.As(err, &blocked):
		writeError(w, http.StatusForbidden, "LICENSE_BLOCKED",
			"a license policy of the target repository blocks "+strings.Join(blocked.Licenses, ", ")+which)
	case errors.Is(err, promote.ErrItemNotFound):
		writeError(w, http.StatusConflict, "artifact_missing", "a bundle item no longer exists in its source repository"+which)
	case errors.Is(err, promote.ErrBundleState), errors.Is(err, promote.ErrStageChanged):
		writeError(w, http.StatusConflict, "conflict", "bundle was changed by a concurrent request; reload it and retry")
	case errors.Is(err, promote.ErrEmpty):
		writeError(w, http.StatusConflict, "conflict", "bundle has no items")
	case errors.Is(err, promote.ErrDockerNotIndexed), errors.Is(err, promote.ErrDockerTagChanged):
		writeError(w, http.StatusConflict, "conflict", "a docker image is not ready to be promoted (its references are not indexed yet, or a tag changed concurrently); retry later"+which)
	case errors.Is(err, promote.ErrFormatMismatch), errors.Is(err, promote.ErrNotHosted),
		errors.Is(err, promote.ErrSameRepository), errors.Is(err, promote.ErrUnsupportedFormat):
		writeError(w, http.StatusBadRequest, "validation_failed", promoteInvalidMessage(err)+which)
	case database.IsUniqueViolation(err):
		writeError(w, http.StatusConflict, "artifact_conflict", "the target repository changed concurrently; retry the promotion")
	default:
		h.log.Errorw("promote bundle failed", "bundle", bundle.Name, "version", bundle.Version, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "promotion failed")
	}
}

func promoteItemLabel(item *promote.ItemError) string {
	label := " (item sha256 " + item.SHA256
	if item.Path != "" {
		label += ", path " + strconv.Quote(item.Path)
	}

	return label + ")"
}

// promoteInvalidMessage is the fixed client message of a promotion request the
// repositories cannot satisfy.
func promoteInvalidMessage(err error) string {
	switch {
	case errors.Is(err, promote.ErrFormatMismatch):
		return "an item's repository and the target repository have different formats"
	case errors.Is(err, promote.ErrSameRepository):
		return "the target repository is the source repository of an item"
	case errors.Is(err, promote.ErrUnsupportedFormat):
		return "promotion is not supported for an item's format"
	default:
		return "bundle promotion needs hosted source and target repositories"
	}
}
