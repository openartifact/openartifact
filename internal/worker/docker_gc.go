// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocimanifest"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	dockerGCRunTimeout = time.Hour

	// dockerGCIndexBatch bounds how many legacy manifests are indexed per query.
	dockerGCIndexBatch = 200
	// dockerGCManifestBatch bounds the manifests deleted per transaction.
	dockerGCManifestBatch = 1000
	// dockerGCBlobBatch bounds the blob records deleted per transaction.
	dockerGCBlobBatch = 500
	// dockerGCPlanLimit bounds the blob records a dry run enumerates.
	dockerGCPlanLimit = 1 << 20
	// DockerGCReportLimit bounds the entries listed in a DockerGCReport.
	DockerGCReportLimit = 1000
)

var (
	// ErrDockerGCBusy means another run is collecting the repository.
	ErrDockerGCBusy = errors.New("docker registry garbage collection is already running for this repository")
	// ErrNotDockerRepo means GC was requested for a repository that is not a docker registry.
	ErrNotDockerRepo = errors.New("repository is not a docker repository")
	// ErrInvalidDockerGCGrace means a negative grace period was requested.
	ErrInvalidDockerGCGrace = errors.New("grace period must not be negative")
)

// DockerGCOptions selects how a registry GC run behaves.
type DockerGCOptions struct {
	// DryRun reports what would be deleted without deleting anything.
	DryRun bool
	// Grace is the minimum age of what may be deleted. Zero means no grace.
	Grace time.Duration
}

// DockerGCManifest is a manifest reported by a GC run.
type DockerGCManifest struct {
	Image  string
	Digest string
	Size   int64
}

// DockerGCBlob is a blob record reported by a GC run.
type DockerGCBlob struct {
	Digest string
	Size   int64
}

// DockerGCReport is the outcome of one registry GC run.
type DockerGCReport struct {
	Repository  string
	DryRun      bool
	GracePeriod time.Duration

	// ManifestsDeleted counts the untagged manifests deleted (or, in a dry run,
	// that would be).
	ManifestsDeleted int
	// BlobRefsDeleted counts the blob records of the repository deleted because
	// no manifest references them any more.
	BlobRefsDeleted int
	// BlobsFreed counts the storage objects actually removed: the ones no other
	// repository, package or manifest still referenced. In a dry run it is the
	// number of storage objects the deleted records point at, an upper bound.
	BlobsFreed int
	// BytesFreed is the size of the BlobsFreed objects.
	BytesFreed int64
	// Failures counts storage deletions that failed and were logged. The rows
	// are already gone, so the orphan collector reclaims their blobs later.
	Failures int

	// Manifests and Blobs list the deleted entries, capped at DockerGCReportLimit.
	Manifests []DockerGCManifest
	Blobs     []DockerGCBlob
	Truncated bool
}

// DockerGCDB is the database a DockerGCWorker needs: the repository and quota
// queries plus the GC store. *database.PostgresDB implements it.
type DockerGCDB interface {
	database.Database
	database.DockerGCStore
}

// DockerGCWorker deletes untagged docker manifests older than a grace period
// and the blobs only they referenced.
//
// A manifest is kept when a tag points at it, when it was pushed within the
// grace period, or when it is tied to a kept manifest: a child of a kept index,
// the subject of a kept referrer, or a referrer of a kept subject. After the
// manifests, the repository's blob records that no manifest references and that
// no client has used within the grace period are deleted, and their storage via
// storage.SafeDeleteBlob (reference counted across repositories).
//
// Safety against concurrent pushes: a run holds a session advisory lock so runs
// do not overlap; every deletion re-checks the same conditions inside a
// transaction that holds the repository's exclusive docker lock, which manifest
// pushes take shared; and recently pushed or used content is never touched.
type DockerGCWorker struct {
	db        DockerGCDB
	blobStore storage.BlobStore
	log       *logger.Logger
	interval  time.Duration
	grace     time.Duration

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	// runCtx is cancelled by Stop so in-flight runs stop promptly.
	runCtx context.Context //nolint:containedctx // lifecycle context of the worker
	cancel context.CancelFunc
}

// NewDockerGCWorker creates the worker. interval is the pause between
// scheduled passes over all docker repositories (0 disables the schedule;
// RunRepo still works for on-demand runs); grace is the default minimum age.
func NewDockerGCWorker(db DockerGCDB, blobStore storage.BlobStore, log *logger.Logger, interval, grace time.Duration) *DockerGCWorker {
	runCtx, cancel := context.WithCancel(context.Background())

	return &DockerGCWorker{
		db:        db,
		blobStore: blobStore,
		log:       log,
		interval:  interval,
		grace:     grace,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		runCtx:    runCtx,
		cancel:    cancel,
	}
}

// DefaultGrace is the configured grace period, used when a run does not
// specify one.
func (w *DockerGCWorker) DefaultGrace() time.Duration { return w.grace }

// Start begins the scheduled passes. With a non-positive interval nothing is
// scheduled.
func (w *DockerGCWorker) Start() {
	w.startOnce.Do(w.start)
}

func (w *DockerGCWorker) start() {
	if w.interval <= 0 {
		close(w.doneCh)
		return
	}

	go func() {
		defer close(w.doneCh)

		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				w.runAllSafely()
			case <-w.stopCh:
				return
			}
		}
	}()
}

// runAllSafely contains a panic to one pass so the schedule keeps running.
func (w *DockerGCWorker) runAllSafely() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in docker gc pass; continuing", "panic", r)
		}
	}()

	w.runAll(w.runCtx)
}

// Stop cancels in-flight runs and waits for the schedule goroutine to exit.
func (w *DockerGCWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.cancel()
	})

	// Never started: nothing will close doneCh, and a later Start must not.
	w.startOnce.Do(func() { close(w.doneCh) })

	<-w.doneCh
}

// runAll collects every docker repository with the configured grace period.
func (w *DockerGCWorker) runAll(ctx context.Context) {
	const pageSize = 100

	for page := 1; ; page++ {
		repos, err := w.db.ListRepositories(ctx, page, pageSize, "docker")
		if err != nil {
			w.log.Errorw("docker gc: list repositories", "error", err)
			return
		}

		for _, repo := range repos {
			if ctx.Err() != nil {
				return
			}

			if _, err := w.RunRepo(ctx, repo, DockerGCOptions{Grace: w.grace}); err != nil && !errors.Is(err, ErrDockerGCBusy) {
				w.log.Errorw("docker gc failed", "repository", repo.Name, "error", err)
			}
		}

		if len(repos) < pageSize {
			return
		}
	}
}

// RunRepo runs registry GC on one docker repository. It stops early, leaving a
// consistent state, when ctx or the worker is cancelled, or after an hour.
func (w *DockerGCWorker) RunRepo(ctx context.Context, repo *models.Repository, opts DockerGCOptions) (*DockerGCReport, error) {
	if repo.Format != "docker" {
		return nil, ErrNotDockerRepo
	}

	if opts.Grace < 0 {
		return nil, ErrInvalidDockerGCGrace
	}

	ctx, cancel := context.WithTimeout(ctx, dockerGCRunTimeout)
	defer cancel()

	stop := context.AfterFunc(w.runCtx, cancel)
	defer stop()

	release, locked, err := tryAdvisoryLock(ctx, w.db.GetDB(), "docker-gc:"+repo.ID)
	if err != nil {
		return nil, fmt.Errorf("lock docker gc: %w", err)
	}

	if !locked {
		return nil, ErrDockerGCBusy
	}
	defer release()

	r := &dockerGCRun{
		w:      w,
		repo:   repo,
		opts:   opts,
		report: &DockerGCReport{Repository: repo.Name, DryRun: opts.DryRun, GracePeriod: opts.Grace},
		freed:  map[string]struct{}{},
	}

	if err := r.run(ctx); err != nil {
		return r.report, err
	}

	if r.report.ManifestsDeleted > 0 || r.report.BlobRefsDeleted > 0 {
		w.log.Infow("docker gc finished",
			"repository", repo.Name, "dry_run", opts.DryRun,
			"manifests", r.report.ManifestsDeleted, "blob_refs", r.report.BlobRefsDeleted,
			"blobs_freed", r.report.BlobsFreed, "bytes_freed", r.report.BytesFreed,
			"failures", r.report.Failures)
	}

	return r.report, nil
}

type dockerGCRun struct {
	w      *DockerGCWorker
	repo   *models.Repository
	opts   DockerGCOptions
	report *DockerGCReport
	// freed holds the storage keys already counted, so a body shared by several
	// manifests is counted once.
	freed map[string]struct{}
}

func (r *dockerGCRun) run(ctx context.Context) error {
	if err := r.indexManifests(ctx); err != nil {
		return err
	}

	manifests, err := r.w.db.ListDockerGCManifests(ctx, r.repo.ID, r.opts.Grace)
	if err != nil {
		return fmt.Errorf("docker gc: %w", err)
	}

	doomed := selectUnreferencedManifests(manifests)

	if r.opts.DryRun {
		return r.planDryRun(ctx, doomed)
	}

	if err := r.deleteManifests(ctx, doomed); err != nil {
		return err
	}

	return r.sweepBlobs(ctx)
}

// indexManifests records the references of manifests that predate
// docker_manifest_refs (or were restored from a backup), reading their bodies
// from the blob store. A body missing from storage references nothing.
func (r *dockerGCRun) indexManifests(ctx context.Context) error {
	for {
		batch, err := r.w.db.ListUnindexedDockerManifests(ctx, r.repo.ID, dockerGCIndexBatch)
		if err != nil {
			return fmt.Errorf("docker gc: %w", err)
		}

		if len(batch) == 0 {
			return nil
		}

		for _, m := range batch {
			refs, err := r.w.readManifestRefs(ctx, m.BlobSHA256)
			if err != nil {
				return fmt.Errorf("docker gc: index manifest %s: %w", m.Digest, err)
			}

			if err := r.w.db.SetDockerManifestRefs(ctx, r.repo.ID, m, refs); err != nil {
				return fmt.Errorf("docker gc: %w", err)
			}
		}
	}
}

func (w *DockerGCWorker) readManifestRefs(ctx context.Context, sha string) (models.DockerManifestRefs, error) {
	rc, err := w.blobStore.Get(ctx, sha)
	if err != nil {
		if ok, existsErr := w.blobStore.Exists(ctx, sha); existsErr == nil && !ok {
			return models.DockerManifestRefs{}, nil
		}

		return models.DockerManifestRefs{}, fmt.Errorf("read manifest body: %w", err)
	}

	defer func() { _ = rc.Close() }()

	body, err := io.ReadAll(io.LimitReader(rc, 16<<20))
	if err != nil {
		return models.DockerManifestRefs{}, fmt.Errorf("read manifest body: %w", err)
	}

	return ocimanifest.ParseRefs(body), nil
}

// planDryRun reports what a real run would delete, deleting nothing.
func (r *dockerGCRun) planDryRun(ctx context.Context, doomed []*models.DockerGCManifest) error {
	ids := make([]string, len(doomed))
	for i, m := range doomed {
		ids[i] = m.ID
		r.addManifest(m)
		r.countFreed(m.BlobSHA256, m.Size)
	}

	blobs, err := r.w.db.ListDockerGCBlobs(ctx, r.repo.ID, r.opts.Grace, ids, dockerGCPlanLimit)
	if err != nil {
		return fmt.Errorf("docker gc: %w", err)
	}

	for _, b := range blobs {
		r.addBlob(b)
		r.countFreed(b.BlobSHA256, b.Size)
	}

	return nil
}

func (r *dockerGCRun) deleteManifests(ctx context.Context, doomed []*models.DockerGCManifest) error {
	for chunk := range slices.Chunk(doomed, dockerGCManifestBatch) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("docker gc: %w", err)
		}

		ids := make([]string, len(chunk))
		for i, m := range chunk {
			ids[i] = m.ID
		}

		deleted, err := r.w.db.DeleteDockerManifestsForGC(ctx, r.repo.ID, ids, r.opts.Grace)
		if err != nil {
			return fmt.Errorf("docker gc: %w", err)
		}

		// Only the rows this transaction removed give quota back: each was
		// metered as its body's bytes and one artifact.
		var freed int64
		for _, m := range deleted {
			freed += m.Size
		}

		r.releaseQuota(ctx, freed, len(deleted))

		for _, m := range deleted {
			r.addManifest(m)
			r.releaseBlob(ctx, m.BlobSHA256, m.Size)
		}
	}

	return nil
}

// releaseQuota returns the usage of rows registry GC just deleted from the
// repository: size bytes and count artifacts (0 for layer blob records, which
// are bytes only).
func (r *dockerGCRun) releaseQuota(ctx context.Context, size int64, count int) {
	if err := quota.ReleaseN(ctx, r.w.db, r.repo.ID, size, count); err != nil {
		r.report.Failures++

		r.w.log.Warnw("docker gc: release quota failed", "repository", r.repo.Name, "error", err)
	}
}

func (r *dockerGCRun) sweepBlobs(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("docker gc: %w", err)
		}

		candidates, err := r.w.db.ListDockerGCBlobs(ctx, r.repo.ID, r.opts.Grace, nil, dockerGCBlobBatch)
		if err != nil {
			return fmt.Errorf("docker gc: %w", err)
		}

		if len(candidates) == 0 {
			return nil
		}

		digests := make([]string, len(candidates))
		for i, b := range candidates {
			digests[i] = b.Digest
		}

		deleted, err := r.w.db.DeleteDockerBlobsForGC(ctx, r.repo.ID, digests, r.opts.Grace)
		if err != nil {
			return fmt.Errorf("docker gc: %w", err)
		}

		if len(deleted) == 0 {
			return nil // candidates became referenced meanwhile; the next run retries
		}

		var freed int64
		for _, b := range deleted {
			freed += b.Size
		}

		r.releaseQuota(ctx, freed, 0)

		for _, b := range deleted {
			r.addBlob(b)
			r.releaseBlob(ctx, b.BlobSHA256, b.Size)
		}
	}
}

// releaseBlob drops a storage object whose docker record was just deleted. It
// is reference counted across repositories and formats, so the object survives
// while anything else uses it.
func (r *dockerGCRun) releaseBlob(ctx context.Context, sha string, size int64) {
	if err := storage.SafeDeleteBlob(ctx, r.w.db, r.w.blobStore, sha); err != nil {
		r.report.Failures++

		r.w.log.Warnw("docker gc: delete blob failed", "repository", r.repo.Name, "sha256", sha, "error", err)

		return
	}

	still, err := r.w.db.BlobRowExists(ctx, sha)
	if err != nil {
		r.report.Failures++

		r.w.log.Warnw("docker gc: check blob failed", "repository", r.repo.Name, "sha256", sha, "error", err)

		return
	}

	if !still {
		r.countFreed(sha, size)
	}
}

func (r *dockerGCRun) countFreed(sha string, size int64) {
	if _, dup := r.freed[sha]; dup {
		return
	}

	r.freed[sha] = struct{}{}
	r.report.BlobsFreed++
	r.report.BytesFreed += size
}

func (r *dockerGCRun) addManifest(m *models.DockerGCManifest) {
	r.report.ManifestsDeleted++

	if len(r.report.Manifests) >= DockerGCReportLimit {
		r.report.Truncated = true
		return
	}

	r.report.Manifests = append(r.report.Manifests, DockerGCManifest{Image: m.Image, Digest: m.Digest, Size: m.Size})
}

func (r *dockerGCRun) addBlob(b *models.DockerGCBlob) {
	r.report.BlobRefsDeleted++

	if len(r.report.Blobs) >= DockerGCReportLimit {
		r.report.Truncated = true
		return
	}

	r.report.Blobs = append(r.report.Blobs, DockerGCBlob{Digest: b.Digest, Size: b.Size})
}

type imageDigest struct{ image, digest string }

// selectUnreferencedManifests returns the manifests that may be deleted: all
// that are not kept. Kept manifests are the tagged and the young ones plus
// whatever they are tied to, transitively and within one image: the children
// of a kept index, the subject of a kept referrer and the referrers of a kept
// subject. Indexes come first in the result so a chunked delete never sees a
// still-listed parent for an already-deleted child.
func selectUnreferencedManifests(manifests []*models.DockerGCManifest) []*models.DockerGCManifest {
	byKey := make(map[imageDigest]int, len(manifests))
	referrers := make(map[imageDigest][]int)

	for i, m := range manifests {
		byKey[imageDigest{m.Image, m.Digest}] = i

		if m.SubjectDigest != "" {
			subject := imageDigest{m.Image, m.SubjectDigest}
			referrers[subject] = append(referrers[subject], i)
		}
	}

	kept := make([]bool, len(manifests))

	var queue []int

	mark := func(i int) {
		if !kept[i] {
			kept[i] = true

			queue = append(queue, i)
		}
	}

	for i, m := range manifests {
		if m.Tagged || m.Young {
			mark(i)
		}
	}

	for len(queue) > 0 {
		m := manifests[queue[len(queue)-1]]
		queue = queue[:len(queue)-1]

		for _, child := range m.Children {
			if j, ok := byKey[imageDigest{m.Image, child}]; ok {
				mark(j)
			}
		}

		if m.SubjectDigest != "" {
			if j, ok := byKey[imageDigest{m.Image, m.SubjectDigest}]; ok {
				mark(j)
			}
		}

		for _, j := range referrers[imageDigest{m.Image, m.Digest}] {
			mark(j)
		}
	}

	var indexes, images []*models.DockerGCManifest

	for i, m := range manifests {
		switch {
		case kept[i]:
		case len(m.Children) > 0:
			indexes = append(indexes, m)
		default:
			images = append(images, m)
		}
	}

	return append(indexes, images...)
}
