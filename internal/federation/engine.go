// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Notifier queues the metadata regeneration of replicated changes (the
// RegenQueue of the format's worker). Either method may be a no-op.
type Notifier interface {
	NotifyMaven(repoID, groupID, artifactID string)
	NotifyNPM(packageID string)
}

// Options configures an Engine.
type Options struct {
	Store    *Store
	SQL      *sql.DB
	DB       database.Database
	Blobs    storage.BlobStore
	Log      *logger.Logger
	Webhooks webhook.Publisher
	Notify   Notifier
	Metrics  *Metrics

	PollInterval       time.Duration
	BatchSize          int
	MaxBackoff         time.Duration
	RequestTimeout     time.Duration
	TombstoneRetention time.Duration
	// TrashDefaultDays is the server-wide trash retention applied to
	// repositories without their own (0: applied deletes are permanent).
	TrashDefaultDays int
	// SpoolDir is where blobs of peers are downloaded to, and where an
	// interrupted download waits to be resumed by the next pull (empty: a
	// directory under the OS temp dir). Give every instance its own.
	SpoolDir string
	// MaxPartialBytes caps the total size of the partial downloads kept in
	// the spool (0: DefaultMaxPartialBytes); the least recently written go first.
	MaxPartialBytes int64
	// MaxPartialAge discards a partial download untouched for this long
	// (0: DefaultMaxPartialAge).
	MaxPartialAge time.Duration
}

// Engine applies the changes pulled from peers and runs the pull workers.
type Engine struct {
	Options

	instance uuid.UUID
	wake     chan struct{}
	spool    *spool
	// resumeBackoff is the pause before the first retry of an interrupted
	// blob transfer (0: defaultResumeBackoff).
	resumeBackoff time.Duration

	mu sync.Mutex
	// failures counts, per peer and unit, the pulls that failed on the unit
	// (see handleUnitError).
	failures map[string]int
}

// maxDepth bounds the chain of dependencies applied ahead of a unit (tag ->
// index -> manifests -> subject).
const maxDepth = 8

// NewEngine builds an engine; the instance identity is read from the database.
func NewEngine(ctx context.Context, o Options) (*Engine, error) {
	id, err := o.Store.InstanceID(ctx)
	if err != nil {
		return nil, err
	}

	if o.Log == nil {
		return nil, errEngineNoLogger
	}

	e := &Engine{
		Options: o, instance: id, wake: make(chan struct{}, 1),
		spool: newSpool(o.SpoolDir, o.MaxPartialBytes, o.MaxPartialAge),
	}

	if o.Metrics != nil {
		fn := e.spool.partialBytes
		o.Metrics.partialBytes.Store(&fn)
	}

	return e, nil
}

// Kick asks the worker to look for due peers now (after a peer was added or a
// resync requested on this replica).
func (e *Engine) Kick() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// outcome is the result of applying one log entry.
type outcome int

const (
	outcomeApplied outcome = iota
	outcomeSkipped
	outcomeDeferred
)

func (o outcome) label() string {
	switch o {
	case outcomeApplied:
		return "applied"
	case outcomeSkipped:
		return "skipped"
	default:
		return "deferred"
	}
}

// pullCtx is what applying a peer's entries needs, built once per pull.
type pullCtx struct {
	repo   *RepoInfo
	peer   *Peer
	client *Client
	codec  Codec
	// conflicts counts the local changes the pull superseded.
	conflicts int64
	// repair is set on a full pass: units that compare equal also get their
	// missing local blobs refetched (see repairBlobs).
	repair bool
	// pending holds the units whose local state is a local change about to
	// be superseded (see markConflict).
	pending map[string]struct{}
}

func (pc *pullCtx) markConflict(key string) {
	if pc.pending == nil {
		pc.pending = map[string]struct{}{}
	}

	pc.pending[key] = struct{}{}
}

// takeConflict reports and clears the mark of a unit.
func (pc *pullCtx) takeConflict(key string) bool {
	_, ok := pc.pending[key]
	delete(pc.pending, key)

	return ok
}

// deferError marks a unit that cannot be applied now but is not the peer's
// fault in general: it is set aside and retried on its own backoff.
type deferError struct {
	reason string
	err    error
}

func (d *deferError) Error() string { return d.reason + ": " + d.err.Error() }
func (d *deferError) Unwrap() error { return d.err }

func deferred(reason string, err error) error { return &deferError{reason: reason, err: err} }

// applyEntry applies one log entry of a peer. A newer state of the unit than
// the entry's is fetched and applied instead; an entry the local log already
// supersedes is skipped.
func (e *Engine) applyEntry(ctx context.Context, pc *pullCtx, entry Entry) (outcome, error) {
	if e.superseded(ctx, pc.repo.ID, entry.UnitKey, entry.Stamp) {
		if pc.repair && !entry.Tombstone {
			return e.repairBlobs(ctx, pc, entry)
		}

		return outcomeSkipped, nil
	}

	if entry.Tombstone {
		return e.applyDelete(ctx, pc, entry)
	}

	return e.applyFetched(ctx, pc, entry.UnitKey, 0)
}

// repairBlobs refetches the blobs of a unit the local log holds at exactly the
// peer's stamp whose bytes are gone from the local store (a resync of equal
// state). A unit that is locally newer is left alone.
func (e *Engine) repairBlobs(ctx context.Context, pc *pullCtx, entry Entry) (outcome, error) {
	local, err := e.Store.GetEntry(ctx, e.SQL, pc.repo.ID, entry.UnitKey)
	if err != nil || local.Tombstone || local.Stamp.Compare(entry.Stamp) != 0 {
		return outcomeSkipped, nil
	}

	content, err := pc.codec.Export(ctx, e.SQL, pc.repo.ID, entry.UnitKey)
	if err != nil || content == nil {
		return outcomeSkipped, nil
	}

	staged, err := e.stageBlobs(ctx, pc, entry.UnitKey, content.Blobs)
	if err != nil {
		return outcomeDeferred, err
	}

	defer cleanupStaged(staged)

	if len(staged) == 0 {
		return outcomeSkipped, nil
	}

	blobs, closeBlobs, err := blobSources(content.Blobs, staged)
	if err != nil {
		return 0, err
	}

	defer closeBlobs()

	err = storage.CommitBlobs(ctx, e.DB, e.Blobs, blobs, func(ctx context.Context, _ []string) error {
		_, err := e.recordBlobs(ctx, content.Blobs)

		return err
	})
	if err != nil {
		if errors.Is(err, storage.ErrBlobGone) {
			return outcomeDeferred, deferred("blob_gone", err)
		}

		return 0, err
	}

	return outcomeApplied, nil
}

// blobSources turns the blobs of a unit into CommitBlobs sources: a staged
// blob is read from its temp file, any other (already in the local store) has
// no source and must still exist when the rows are committed. The temp files
// are opened once; the returned func closes them.
func blobSources(refs []BlobRef, staged []stagedBlob) ([]storage.Blob, func(), error) {
	files := make(map[string]*os.File, len(staged))

	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}

	for _, s := range staged {
		if _, ok := files[s.ref.SHA256]; ok {
			continue
		}

		f, err := os.Open(s.path)
		if err != nil {
			closeAll()

			return nil, nil, fmt.Errorf("open staged blob: %w", err)
		}

		files[s.ref.SHA256] = f
	}

	blobs := make([]storage.Blob, 0, len(refs))

	for _, ref := range refs {
		if f, ok := files[ref.SHA256]; ok {
			blobs = append(blobs, storage.SeekerBlob(ref.SHA256, f, nil))

			continue
		}

		blobs = append(blobs, storage.Blob{SHA256: ref.SHA256})
	}

	return blobs, closeAll, nil
}

// superseded reports whether the local log already holds the unit at or past
// the stamp.
func (e *Engine) superseded(ctx context.Context, repoID, key string, stamp Stamp) bool {
	local, err := e.Store.GetEntry(ctx, e.SQL, repoID, key)
	if err != nil {
		return false
	}

	return local.Stamp.Compare(stamp) >= 0
}

// applyFetched fetches a unit's current state from the peer and applies it.
func (e *Engine) applyFetched(ctx context.Context, pc *pullCtx, key string, depth int) (outcome, error) {
	ur, err := pc.client.Unit(ctx, key)
	if errors.Is(err, ErrNotFound) {
		// Gone at the peer since the log page: a newer entry follows.
		return outcomeSkipped, nil
	}

	if errors.Is(err, ErrQuarantined) {
		return outcomeDeferred, deferred("quarantined", err)
	}

	if err != nil {
		return 0, err
	}

	if e.superseded(ctx, pc.repo.ID, key, ur.Entry.Stamp) {
		return outcomeSkipped, nil
	}

	if ur.Entry.Tombstone || ur.Content == nil {
		return e.applyDelete(ctx, pc, ur.Entry)
	}

	return e.applyLive(ctx, pc, ur, depth)
}

// ensureDeps applies the units a unit depends on that the local log lacks.
func (e *Engine) ensureDeps(ctx context.Context, pc *pullCtx, content *Content, depth int) error {
	if len(content.Deps) == 0 {
		return nil
	}

	if depth >= maxDepth {
		return deferred("dependency_depth", errDependencyDepth)
	}

	for _, dep := range content.Deps {
		local, err := e.Store.GetEntry(ctx, e.SQL, pc.repo.ID, dep)
		if err == nil && !local.Tombstone {
			continue
		}

		if err == nil && local.Tombstone {
			return deferred("dependency_deleted", fmt.Errorf("%w: %s", errDependencyDeleted, dep))
		}

		if !errors.Is(err, ErrNotFound) {
			return err
		}

		res, err := e.applyFetched(ctx, pc, dep, depth+1)
		if err != nil {
			return err
		}

		if res == outcomeSkipped {
			return deferred("dependency_missing", fmt.Errorf("%w: %s", errDependencyMissing, dep))
		}
	}

	return nil
}

// stagedBlob is a blob downloaded from the peer and verified, waiting in a
// temp file for the commit.
type stagedBlob struct {
	ref  BlobRef
	path string
}

// stageBlobs downloads the blobs of a unit that the local store lacks into
// verified temp files.
func (e *Engine) stageBlobs(ctx context.Context, pc *pullCtx, key string, blobs []BlobRef) ([]stagedBlob, error) {
	var staged []stagedBlob

	for _, ref := range blobs {
		if err := storage.ValidateBlobID(ref.SHA256); err != nil {
			cleanupStaged(staged)

			return nil, deferred("invalid_blob", err)
		}

		have, err := e.Blobs.Exists(ctx, ref.SHA256)
		if err != nil {
			cleanupStaged(staged)

			return nil, fmt.Errorf("check blob %s: %w", ref.SHA256, err)
		}

		if have {
			continue
		}

		path, err := e.download(ctx, pc, key, ref)
		if err != nil {
			cleanupStaged(staged)

			return nil, err
		}

		if path == "" { // another pull stored it meanwhile
			continue
		}

		staged = append(staged, stagedBlob{ref: ref, path: path})
	}

	return staged, nil
}

func cleanupStaged(staged []stagedBlob) {
	for _, s := range staged {
		_ = os.Remove(s.path)
	}
}

// applyLive applies a live unit: dependencies first, then the blobs, then the
// rows in one transaction that also logs the unit under the remote stamp.
func (e *Engine) applyLive(ctx context.Context, pc *pullCtx, ur *UnitResponse, depth int) (outcome, error) {
	entry, content := ur.Entry, ur.Content

	if content.Kind != entry.Kind {
		return outcomeDeferred, deferred("invalid_unit", fmt.Errorf("%w: unit %s: content kind %q, log kind %q", errUnitKindMismatch, entry.UnitKey, content.Kind, entry.Kind))
	}

	if err := e.ensureDeps(ctx, pc, content, depth); err != nil {
		return outcomeDeferred, err
	}

	e.markConflict(ctx, pc, entry)

	staged, err := e.stageBlobs(ctx, pc, entry.UnitKey, content.Blobs)
	if err != nil {
		return outcomeDeferred, err
	}

	defer cleanupStaged(staged)

	size, count, err := pc.codec.Usage(ctx, e.SQL, pc.repo.ID, entry.UnitKey, content)
	if err != nil {
		return 0, err
	}

	reserved := false

	if size > 0 || count > 0 {
		if err := quota.EnforceN(ctx, e.DB, pc.repo.ID, size, count); err != nil {
			if errors.Is(err, quota.ErrExceeded) {
				return outcomeDeferred, deferred("quota_exceeded", err)
			}

			return 0, err
		}

		reserved = true
	}

	var change *Change

	err = e.commitLive(ctx, pc, entry, content, staged, &change)
	if err != nil {
		if reserved {
			_ = quota.ReleaseN(ctx, e.DB, pc.repo.ID, size, count)
		}

		if errors.Is(err, ErrStale) {
			return outcomeSkipped, nil
		}

		return 0, err
	}

	e.afterLive(ctx, pc, entry, change)

	return outcomeApplied, nil
}

// commitLive stores the staged blobs and applies the unit's rows. The bytes
// are stored first; under the blob locks every blob's presence is checked
// again (a blob that has no staged copy and vanished since staging fails the
// unit with storage.ErrBlobGone, which defers it for a retry), so a concurrent
// delete cannot remove a blob between its storage and the row that references
// it. The locks cover only the database work.
func (e *Engine) commitLive(ctx context.Context, pc *pullCtx, entry Entry, content *Content, staged []stagedBlob, out **Change) error {
	blobs, closeBlobs, err := blobSources(content.Blobs, staged)
	if err != nil {
		return err
	}

	defer closeBlobs()

	err = storage.CommitBlobs(ctx, e.DB, e.Blobs, blobs, func(ctx context.Context, _ []string) error {
		blobIDs, err := e.recordBlobs(ctx, content.Blobs)
		if err != nil {
			return err
		}

		tx, err := e.SQL.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin apply: %w", err)
		}

		defer func() { _ = tx.Rollback() }()

		if err := e.enterApplyMode(ctx, tx); err != nil {
			return err
		}

		change, err := pc.codec.Import(ctx, tx, pc.repo.ID, entry.UnitKey, content, blobIDs)
		if err != nil {
			return err
		}

		if err := e.recordUnit(ctx, tx, pc.repo.ID, entry, false); err != nil {
			return err
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit apply: %w", err)
		}

		*out = change

		return nil
	})
	if errors.Is(err, storage.ErrBlobGone) {
		return deferred("blob_gone", err)
	}

	return err //nolint:wrapcheck // commit errors and *storage.StoreError pass through
}

// enterApplyMode silences the capture triggers for the transaction: applied
// changes are logged with the remote stamp by recordUnit, not as local ones.
func (e *Engine) enterApplyMode(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `SELECT set_config('oa.federation_apply', 'on', true)`); err != nil {
		return fmt.Errorf("enter apply mode: %w", err)
	}

	return nil
}

// recordUnit logs the unit under its remote stamp; ErrStale when a newer state
// won meanwhile (the caller's transaction is then abandoned).
func (e *Engine) recordUnit(ctx context.Context, tx *sql.Tx, repoID string, entry Entry, tombstone bool) error {
	var applied bool

	err := tx.QueryRowContext(ctx,
		`SELECT federation_apply_unit($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9::uuid)`,
		repoID, entry.UnitKey, entry.Kind, entry.Event, entry.ContentRef, tombstone,
		entry.Stamp.Wall, entry.Stamp.Counter, entry.Stamp.Origin.String(),
	).Scan(&applied)
	if err != nil {
		return fmt.Errorf("log applied unit: %w", err)
	}

	if !applied {
		return ErrStale
	}

	return nil
}

// recordBlobs makes sure every blob of the unit has a blobs row; it returns
// sha256 -> blobs.id. It is database work only, run by the CommitBlobs commit
// once the objects are in the store.
func (e *Engine) recordBlobs(ctx context.Context, refs []BlobRef) (map[string]string, error) {
	ids := make(map[string]string, len(refs))

	for _, ref := range refs {
		media := ref.MediaType
		if media == "" {
			media = "application/octet-stream"
		}

		id, err := e.DB.EnsureBlob(ctx, ref.SHA256, media, ref.Size)
		if err != nil {
			return nil, fmt.Errorf("record blob %s: %w", ref.SHA256, err)
		}

		ids[ref.SHA256] = id
	}

	return ids, nil
}

// afterLive does the work that follows a committed live apply: quota and blob
// cleanup of a replaced row, metadata regeneration, the webhook.
func (e *Engine) afterLive(ctx context.Context, pc *pullCtx, entry Entry, change *Change) {
	if change == nil {
		return
	}

	e.countConflict(ctx, pc, entry)

	if change.Replaced && (change.ReplacedSize > 0 || change.ReplacedCount > 0) {
		if err := quota.ReleaseN(ctx, e.DB, pc.repo.ID, change.ReplacedSize, change.ReplacedCount); err != nil {
			e.Log.Warnw("federation: release quota of replaced unit", "repo", pc.repo.Name, "unit", entry.UnitKey, "error", err)
		}
	}

	e.releaseBlobs(ctx, change.ReleasedBlobs)
	e.notify(change.Regen)
	e.publish(ctx, pc, webhook.EventArtifactUploaded, change.Artifact)
}

// countConflict records that a pull superseded a change made on this instance.
// Called after the commit, so the pre-image is gone; the decision was taken
// from the local log before applying (see markConflict).
func (e *Engine) countConflict(_ context.Context, pc *pullCtx, entry Entry) {
	if pc.takeConflict(entry.UnitKey) {
		pc.conflicts++

		if e.Metrics != nil {
			e.Metrics.Conflicts.WithLabelValues(pc.repo.Name, pc.peer.Name).Inc()
		}

		e.Log.Warnw("federation: local change superseded by peer (last writer wins)",
			"repo", pc.repo.Name, "peer", pc.peer.Name, "unit", entry.UnitKey)
	}
}

func (e *Engine) releaseBlobs(ctx context.Context, shas []string) {
	for _, sha := range slices.Compact(slices.Sorted(slices.Values(shas))) {
		if err := storage.SafeDeleteBlob(ctx, e.DB, e.Blobs, sha); err != nil {
			e.Log.Warnw("federation: release blob", "sha256", sha, "error", err)
		}
	}
}

func (e *Engine) notify(regen []Regen) {
	if e.Notify == nil {
		return
	}

	for _, r := range regen {
		switch r.Kind {
		case kindMaven:
			e.Notify.NotifyMaven(r.RepoID, r.GroupID, r.ArtifactID)
		case kindNPM:
			e.Notify.NotifyNPM(r.PackageID)
		}
	}
}

func (e *Engine) publish(ctx context.Context, pc *pullCtx, event string, a *ArtifactInfo) {
	if e.Webhooks == nil || a == nil {
		return
	}

	webhook.Emit(ctx, e.Webhooks, pc.repo.ID, event, webhook.ArtifactPayload(event, webhook.Artifact{
		Repository: pc.repo.Name, Format: pc.repo.Format, Name: a.Name, Version: a.Version,
		Path: a.Path, SHA256: a.SHA256, Size: a.Size,
	}))
}

// applyDelete applies a tombstone: the live row (if any) goes to the trash, or
// is removed when the repository keeps no trash, and the tombstone is logged
// under the remote stamp so an older copy of the unit can never come back.
func (e *Engine) applyDelete(ctx context.Context, pc *pullCtx, entry Entry) (outcome, error) {
	days, err := e.Store.TrashRetentionDays(ctx, e.SQL, pc.repo.ID, e.TrashDefaultDays)
	if err != nil {
		return 0, err
	}

	soft := days > 0

	var change *Change

	e.markConflict(ctx, pc, entry)

	err = func() error {
		tx, err := e.SQL.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin apply delete: %w", err)
		}

		defer func() { _ = tx.Rollback() }()

		if err := e.enterApplyMode(ctx, tx); err != nil {
			return err
		}

		change, err = pc.codec.Delete(ctx, tx, pc.repo.ID, entry.UnitKey, soft, "federation:"+pc.peer.Name)
		if err != nil {
			return err
		}

		if err := e.recordUnit(ctx, tx, pc.repo.ID, entry, true); err != nil {
			return err
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit apply delete: %w", err)
		}

		return nil
	}()
	if errors.Is(err, ErrStale) {
		return outcomeSkipped, nil
	}

	if err != nil {
		return 0, err
	}

	e.countConflict(ctx, pc, entry)
	e.afterDelete(ctx, pc, entry, change, soft)

	return outcomeApplied, nil
}

func (e *Engine) afterDelete(ctx context.Context, pc *pullCtx, entry Entry, change *Change, soft bool) {
	if change == nil || change.Missing {
		return
	}

	if !soft {
		if err := quota.ReleaseN(ctx, e.DB, pc.repo.ID, change.RemovedSize, change.RemovedCount); err != nil {
			e.Log.Warnw("federation: release quota of deleted unit", "repo", pc.repo.Name, "unit", entry.UnitKey, "error", err)
		}

		e.releaseBlobs(ctx, change.ReleasedBlobs)
	}

	e.notify(change.Regen)
	e.publish(ctx, pc, webhook.EventArtifactDeleted, change.Artifact)
}

// markConflict remembers, before an apply replaces it, that the unit's local
// state is a change made on this instance that differs from the incoming one.
func (e *Engine) markConflict(ctx context.Context, pc *pullCtx, incoming Entry) {
	local, err := e.Store.GetEntry(ctx, e.SQL, pc.repo.ID, incoming.UnitKey)
	if err != nil || local.Tombstone || local.Stamp.Origin != e.instance {
		return
	}

	if incoming.Tombstone || local.ContentRef != incoming.ContentRef {
		pc.markConflict(incoming.UnitKey)
	}
}
