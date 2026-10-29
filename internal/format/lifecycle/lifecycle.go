// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package lifecycle is where a format says what has to happen to its generated
// metadata when rows change behind the handler's back: a repository is rebuilt
// from a backup or its signing key changes (Stale), a cleanup policy removes
// rows (Removed), a trashed item is restored (Restored).
//
// It is a leaf package: internal/worker, internal/api, internal/gpgkeys and
// cmd/server look hooks up here, internal/format/plugin fills the registry
// from each format's registration, and nothing in it knows a format by name.
// The metadata workers reach a hook through Env.Workers, which the process
// fills with the workers it runs; a hook whose worker is absent (the feature is
// off, or this process does not run it) does nothing.
package lifecycle

import (
	"context"
	"database/sql"
	"reflect"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/registry"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Env is what a hook may use. A caller fills the fields it has; a hook that
// needs one the caller did not provide skips that step.
type Env struct {
	Log     *logger.Logger
	Workers *Workers

	// MarkRepoStale flags a repository's generated metadata as stale in the
	// database, so the stale sweep recovers the refresh when the notification is
	// lost (rpm).
	MarkRepoStale func(ctx context.Context, repoID string) error
	// MarkPackumentStale flips an npm package's stale marker, moving its ETag.
	MarkPackumentStale func(ctx context.Context, packageID string) error
	// SQL runs the lookups a hook needs to find what to refresh.
	SQL *sql.DB
}

// Removal describes the rows one cleanup policy run removed from a repository.
type Removal struct {
	Repo *models.Repository
	// Names are the package names (the format's listing name) the run removed
	// versions of.
	Names []string
	// Hard is the number of rows deleted for good, as opposed to moved to the
	// trash.
	Hard int
}

// Restoration describes one trash item that was restored into a repository.
type Restoration struct {
	RepositoryID   string
	RepositoryName string
	Kind           models.TrashKind
	// RegenKeys resolves what the metadata workers key a refresh of the item by
	// (a package id, or group and artifact); nil when the store cannot.
	RegenKeys func(ctx context.Context) (string, string, error)
}

// Change names what one refresh of a format's generated metadata is keyed by.
// The engines that copy or restore rows in bulk (promotion, backup restore)
// know which keys they touched; the format's hook picks the one it refreshes by.
type Change struct {
	// RepoID is the repository whose rows changed.
	RepoID string
	// PackageID is the package row that changed, when the engine knows it.
	PackageID string
	// GroupID and ArtifactID are the Maven coordinates, when the engine knows them.
	GroupID, ArtifactID string
}

// Hooks is the lifecycle of one format. A nil function is a step the format
// does not have: its indexes are computed live from the tables.
type Hooks struct {
	// Stale refreshes all of a repository's generated metadata: after a
	// rebuild from a backup, and when the repository's signing key changed so
	// its signatures must be redone. A failure is returned; the caller decides
	// whether it matters.
	Stale func(ctx context.Context, env *Env, repoID string) error
	// Removed follows up a cleanup run that removed rows. Failures are logged.
	Removed func(ctx context.Context, env *Env, r Removal)
	// Changed follows up rows an engine wrote straight into a repository
	// (promotion, backup restore): it only enqueues the refresh of what the
	// Change keys. env carries the workers and no log.
	Changed func(env *Env, c Change)
	// Restored follows up the restore of a trash item. Failures are logged:
	// the restore itself already succeeded.
	Restored func(ctx context.Context, env *Env, r Restoration)
}

// None is the lifecycle of a format whose indexes are all computed live.
//
//nolint:gochecknoglobals // immutable zero value, named so a registration states the choice
var None = Hooks{}

// hooks holds the Hooks of every registered format.
var hooks registry.Registry[Hooks] //nolint:gochecknoglobals // the format registry, filled by Register at startup

// Register records the lifecycle of format. It panics when format is
// registered twice.
func Register(format string, h Hooks) { hooks.Register(format, h) }

// Lookup returns the lifecycle registered for format.
func Lookup(format string) (Hooks, bool) { return hooks.Lookup(format) }

// Notify runs the Changed hook of format over the workers w. It does nothing
// when w is nil (the engine has no workers wired) or the format has no hook.
func Notify(w *Workers, format string, c Change) {
	if w == nil {
		return
	}

	if h, ok := Lookup(format); ok && h.Changed != nil {
		h.Changed(&Env{Workers: w}, c)
	}
}

// Workers are the metadata workers a process runs, by format. It is filled at
// startup, before the hooks run, and read-only afterwards.
type Workers struct {
	m map[string]any
}

// NewWorkers returns an empty set.
func NewWorkers() *Workers { return &Workers{m: map[string]any{}} }

// Bind records the worker of format. A nil worker (typed or not) is not bound:
// the worker is not running in this process.
func (w *Workers) Bind(format string, worker any) {
	if worker == nil {
		return
	}

	if v := reflect.ValueOf(worker); v.Kind() == reflect.Pointer && v.IsNil() {
		return
	}

	w.m[format] = worker
}

// Worker returns the worker bound for format as the narrow interface T the
// caller needs. It reports false when none is bound or it does not implement T.
func Worker[T any](w *Workers, format string) (T, bool) {
	var zero T

	if w == nil {
		return zero, false
	}

	v, ok := w.m[format].(T)

	return v, ok
}
