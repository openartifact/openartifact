// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package trash is the policy layer of soft delete: format handlers route
// deletes through Delete (soft when the repository's retention allows it, the
// caller's own hard delete otherwise), guard released versions with
// GuardRelease, and the trash API and purge worker use the audit helpers so
// every delete/restore/purge leaves the same trail.
package trash

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

// ErrImmutableRelease is returned by GuardRelease when the repository has
// immutable_releases set and the operation would delete or overwrite a
// released version. Handlers map it to 409 Conflict.
var ErrImmutableRelease = errors.New("released versions are immutable in this repository; an admin may override with force")

// evaluator decides whether a principal is an admin (global `manage`). It is
// set once at startup; without it force is never honoured.
//
//nolint:gochecknoglobals // process-lifetime singleton, like audit.SetDefault
var evaluator atomic.Pointer[auth.Evaluator]

// SetEvaluator installs the permission evaluator used to honour `force`.
func SetEvaluator(e *auth.Evaluator) { evaluator.Store(e) }

// Actor returns the username of the principal behind ctx, "" when anonymous or
// when no principal is attached (background jobs).
func Actor(ctx context.Context) string {
	p := middleware.PrincipalFromContext(ctx)
	if p == nil || p.IsAnonymous {
		return ""
	}

	return p.Username
}

// IsAdmin reports whether the request's principal holds the global `manage`
// permission.
func IsAdmin(ctx context.Context) bool {
	e := evaluator.Load()

	p := middleware.PrincipalFromContext(ctx)
	if e == nil || p == nil || p.IsAnonymous {
		return false
	}

	ok, err := e.Evaluate(ctx, *p, "", "", "manage")

	return err == nil && ok
}

// ForceRequested reports whether the request asks to override immutability
// (`?force=true`). It is only honoured for admins, see GuardRelease.
func ForceRequested(r *http.Request) bool {
	switch strings.ToLower(r.URL.Query().Get("force")) {
	case "1", "true", "yes":
		return true
	}

	return false
}

// GuardRelease rejects a delete or overwrite of version when repo is
// immutable and version is a release of its format (see IsRelease). An admin
// who sent `?force=true` is let through. Pass the version being deleted or
// overwritten; formats without versions pass "".
func GuardRelease(r *http.Request, repo *models.Repository, format, version string) error {
	if repo == nil || !repo.ImmutableReleases || !IsRelease(format, version) {
		return nil
	}

	if ForceRequested(r) && IsAdmin(r.Context()) {
		return nil
	}

	return ErrImmutableRelease
}

// GuardReleaseByName is GuardRelease for handlers that only have the
// repository name.
func GuardReleaseByName(r *http.Request, db database.Database, repoName, format, version string) error {
	repo, err := db.GetRepositoryByName(r.Context(), repoName)
	if err != nil {
		return fmt.Errorf("look up repository: %w", err)
	}

	return GuardRelease(r, repo, format, version)
}

// Request describes the rows of one delete.
type Request struct {
	RepoID string
	Kind   models.TrashKind
	// IDs are the primary keys of the rows to delete in the kind's table.
	IDs []string
	// Resource is a human-readable description for the audit trail
	// (e.g. "com/acme/lib/1.0/lib-1.0.jar").
	Resource string
}

// Delete deletes the rows of req: it moves them to the trash when the
// repository's retention allows, and otherwise runs hard, the caller's
// existing permanent delete (DB rows, quota release, blob cleanup). It records
// one artifact.deleted audit event either way. A database without trash
// support always takes the hard path.
//
// database.ErrTrashNotFound means the rows are no longer live in the
// repository; handlers answer 404.
func Delete(ctx context.Context, db database.Database, req Request, hard func() error) error {
	soft := false

	if store, ok := db.(database.TrashStore); ok {
		var err error

		soft, err = store.TrashRows(ctx, req.RepoID, req.Kind, req.IDs, Actor(ctx))
		if err != nil {
			return fmt.Errorf("move to trash: %w", err)
		}
	}

	if !soft {
		if err := hard(); err != nil {
			return err
		}
	}

	verb := "deleted"
	if soft {
		verb = "moved to trash"
	}

	auditEvent(ctx, audit.ActionArtifactDeleted, req.RepoID, string(req.Kind), firstOrEmpty(req.IDs), verb+": "+req.Resource)

	return nil
}

func firstOrEmpty(ids []string) string {
	if len(ids) == 0 {
		return ""
	}

	return ids[0]
}

// AuditRestored records that item left the trash.
func AuditRestored(ctx context.Context, item *models.TrashItem) {
	action := audit.ActionArtifactRestored
	if item.Kind == models.TrashKindRepository {
		action = audit.ActionRepositoryRestored
	}

	auditEvent(ctx, action, item.RepositoryID, string(item.Kind), item.ID, "restored: "+Describe(item))
}

// AuditPurged records that a trashed item was permanently removed. ctx may
// carry no principal (the retention purge): the event is then attributed to
// the system.
func AuditPurged(ctx context.Context, res *models.TrashPurgeResult, reason string) {
	action := audit.ActionArtifactPurged
	if res.Kind == models.TrashKindRepository {
		action = audit.ActionRepositoryPurged
	}

	desc := res.Name
	if res.Version != "" {
		desc += " " + res.Version
	}

	repoID := res.RepositoryID
	if res.Kind == models.TrashKindRepository {
		// The repository row is gone: audit_logs.repository_id would violate
		// its foreign key. The name stays in the message, the id in resource_id.
		repoID = ""
	}

	auditEvent(ctx, action, repoID, string(res.Kind), res.ID, fmt.Sprintf("purged (%s): %s", reason, desc))
}

// Describe renders an item as "name version" for audit messages.
func Describe(item *models.TrashItem) string {
	if item.Version == "" {
		return item.Name
	}

	return item.Name + " " + item.Version
}

func auditEvent(ctx context.Context, action, repoID, resourceType, resourceID, details string) {
	l := audit.Default()
	if l == nil {
		return
	}

	ev := audit.AuditEvent{
		Action:       action,
		ActorType:    "system",
		Details:      details,
		ResourceType: &resourceType,
	}

	if repoID != "" {
		ev.RepositoryID = &repoID
	}

	if resourceID != "" {
		ev.ResourceID = &resourceID
	}

	if p := middleware.PrincipalFromContext(ctx); p != nil && !p.IsAnonymous {
		ev.ActorType = "user"
		ev.Actor = p.Username
		ev.ActorID = p.UserID

		if p.IsToken() {
			ev.ActorType = "token"
		}
	}

	l.Log(ctx, ev)
}
