// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package vexsync imports VEX decisions made elsewhere: the vendor CSAF VEX
// feeds an administrator configures, materialized onto the scanned artifacts
// they speak about, and the decisions of federated peers, kept as suggestions
// and applied where the peer is trusted with them. It also builds the document
// of a repository's own decisions that its peers fetch.
package vexsync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/csaf"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/pglock"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// feedLockPrefix prefixes the cluster-wide advisory lock of one feed, held
	// while it is synced or materialized, so a web and a worker process never
	// work on the same feed at once.
	feedLockPrefix = "openartifact:vex-feed:"

	// syncTimeout bounds one sync started by StartSync: the fetch of every
	// changed document, their storage and the materialization.
	syncTimeout = 30 * time.Minute
)

// ErrSyncRunning is returned by SyncFeed when a sync of the feed is already
// running, in this process or another one.
var ErrSyncRunning = errors.New("a sync of the VEX feed is already running")

// Store is the part of the database the service needs.
type Store interface {
	ListVEXFeeds(ctx context.Context) ([]*models.VEXFeed, error)
	GetVEXFeed(ctx context.Context, id string) (*models.VEXFeed, error)
	ReplaceVEXFeedDocument(ctx context.Context, feedID, documentURL string, stmts []models.VEXFeedStatement) error
	ListVEXFeedStatements(ctx context.Context, feedID string) ([]models.VEXFeedStatement, error)
	RecordVEXFeedSync(ctx context.Context, feedID string, res models.VEXFeedSyncResult) error

	ListVEXTargets(ctx context.Context, repoID string) ([]models.VEXTarget, error)
	ListVEXStatements(ctx context.Context, repoID, artifactID string) ([]*models.VEXStatement, error)
	ReplaceFeedVEXStatements(ctx context.Context, feedID string, stmts []*models.VEXStatement) (int, error)
	ApplyPeerVEXStatement(ctx context.Context, s *models.VEXStatement) (bool, error)

	UpsertVEXSuggestion(ctx context.Context, s *models.VEXSuggestion) (bool, error)
	SetVEXSuggestionState(ctx context.Context, repoID, id, state, decidedBy string) error
	PruneVEXSuggestions(ctx context.Context, peerID string, keepIDs []string) (int64, error)
}

// Service syncs the vendor VEX feeds, materializes their statements and
// exchanges VEX decisions with federated peers.
type Service struct {
	db      Store
	fetcher *csaf.Fetcher
	gen     *sbom.Generator
	log     *logger.Logger
	sqlDB   *sql.DB

	// ctx is the context of the syncs StartSync runs; Stop cancels it and
	// waits for them.
	ctx    context.Context //nolint:containedctx // lifecycle context of the background syncs
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// running holds the feeds this process is syncing.
	mu      sync.Mutex
	running map[string]struct{}
}

// NewService returns the service. Without WithClusterLock there are no other
// replicas to exclude.
func NewService(db Store, fetcher *csaf.Fetcher, gen *sbom.Generator, log *logger.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())

	return &Service{
		db: db, fetcher: fetcher, gen: gen, log: log,
		ctx: ctx, cancel: cancel,
		running: make(map[string]struct{}),
	}
}

// WithClusterLock makes the syncs and materializations of a feed take a
// Postgres advisory lock on a pinned connection of sqlDB, so one replica at a
// time works on a feed.
func (s *Service) WithClusterLock(sqlDB *sql.DB) *Service {
	s.sqlDB = sqlDB

	return s
}

// StartSync syncs the feed in the background, under a fresh context bounded by
// syncTimeout, and reports whether it started: false when this process is
// already syncing the feed.
func (s *Service) StartSync(feedID string) bool {
	if !s.claim(feedID) {
		return false
	}

	s.wg.Go(func() {
		defer s.unclaim(feedID)
		defer func() {
			if r := recover(); r != nil {
				s.log.Errorw("panic in VEX feed sync", "feed_id", feedID, "panic", r)
			}
		}()

		ctx, cancel := context.WithTimeout(s.ctx, syncTimeout)
		defer cancel()

		if _, err := s.syncClaimed(ctx, feedID); err != nil && s.ctx.Err() == nil {
			s.log.Errorw("VEX feed sync failed", "feed_id", feedID, "error", err)
		}
	})

	return true
}

// Stop cancels the syncs StartSync started and waits for them to end.
func (s *Service) Stop() {
	s.cancel()
	s.wg.Wait()
}

func (s *Service) claim(feedID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.running[feedID]; ok {
		return false
	}

	s.running[feedID] = struct{}{}

	return true
}

func (s *Service) unclaim(feedID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.running, feedID)
}

// lock takes the advisory lock of the feed; acquired is false when another
// session holds it.
func (s *Service) lock(ctx context.Context, feedID string) (func(), bool, error) {
	if s.sqlDB == nil {
		return func() {}, true, nil
	}

	release, acquired, err := pglock.Try(ctx, s.sqlDB, feedLockPrefix+feedID)
	if err != nil {
		return nil, false, fmt.Errorf("lock VEX feed %s: %w", feedID, err)
	}

	return release, acquired, nil
}

// SyncFeed fetches the documents of the feed changed since its cursor, stores
// them, records the outcome on the feed and materializes its statements. A
// fetch that failed, or rejected documents, are in the result (and on the
// feed); the error is non-nil when the sync could not run or be recorded, and
// ErrSyncRunning when the feed is already being synced.
func (s *Service) SyncFeed(ctx context.Context, feedID string) (models.VEXFeedSyncResult, error) {
	if !s.claim(feedID) {
		return models.VEXFeedSyncResult{}, ErrSyncRunning
	}
	defer s.unclaim(feedID)

	return s.syncClaimed(ctx, feedID)
}

func (s *Service) syncClaimed(ctx context.Context, feedID string) (models.VEXFeedSyncResult, error) {
	release, acquired, err := s.lock(ctx, feedID)
	if err != nil {
		return models.VEXFeedSyncResult{}, err
	}

	if !acquired {
		return models.VEXFeedSyncResult{}, ErrSyncRunning
	}
	defer release()

	feed, err := s.db.GetVEXFeed(ctx, feedID)
	if err != nil {
		return models.VEXFeedSyncResult{}, fmt.Errorf("load VEX feed: %w", err)
	}

	res, err := s.fetch(ctx, feed)
	if err != nil {
		return res, err
	}

	if err := s.db.RecordVEXFeedSync(ctx, feed.ID, res); err != nil {
		return res, fmt.Errorf("record VEX feed sync: %w", err)
	}

	s.log.Infow("VEX feed synced", "feed", feed.Name, "success", res.Success,
		"documents", res.Documents, "rejected", res.Rejected, "error", res.Error)

	if !res.Success {
		return res, nil
	}

	targets, err := s.db.ListVEXTargets(ctx, "")
	if err != nil {
		return res, fmt.Errorf("list VEX targets: %w", err)
	}

	return res, s.applyFeed(ctx, feed, newTargetIndex(targets))
}

// fetch downloads the changed documents of the feed and stores each one. A
// failed download is a result without success; only a storage failure is an
// error. The cursor advances past rejected documents too (they are counted
// and named on the feed), so one broken document does not make every later
// sync download the whole history again.
func (s *Service) fetch(ctx context.Context, feed *models.VEXFeed) (models.VEXFeedSyncResult, error) {
	out, err := s.fetcher.Fetch(ctx, csaf.Source{
		URL: feed.URL, RequireSignature: feed.RequireSignature, PGPKeys: feed.PGPKeys, Since: feed.CursorAt,
	})
	if err != nil {
		if ctx.Err() != nil {
			return models.VEXFeedSyncResult{}, fmt.Errorf("fetch VEX feed: %w", err)
		}

		// The fetcher's errors are fixed, client-safe texts.
		return models.VEXFeedSyncResult{Error: err.Error()}, nil
	}

	for _, doc := range out.Documents {
		if err := s.db.ReplaceVEXFeedDocument(ctx, feed.ID, doc.URL, doc.Statements); err != nil {
			return models.VEXFeedSyncResult{}, fmt.Errorf("store VEX feed document: %w", err)
		}
	}

	return models.VEXFeedSyncResult{
		Success:   true,
		Error:     rejectionSummary(out.Rejected),
		CursorAt:  out.Cursor,
		Documents: len(out.Documents),
		Rejected:  len(out.Rejected),
	}, nil
}

// rejectionSummary is the feed error of a sync that rejected documents: their
// count and the first reason (the fetcher's reasons are client-safe).
func rejectionSummary(rejected []csaf.Rejection) string {
	switch len(rejected) {
	case 0:
		return ""
	case 1:
		return "1 document rejected: " + rejected[0].Reason
	}

	return fmt.Sprintf("%d documents rejected: %s", len(rejected), rejected[0].Reason)
}

// ApplyFeeds materializes the statements of every feed onto the latest scans,
// so new scans and new artifacts pick up the feeds' decisions. A feed being
// synced elsewhere is skipped: its sync materializes it.
func (s *Service) ApplyFeeds(ctx context.Context) error {
	feeds, err := s.db.ListVEXFeeds(ctx)
	if err != nil {
		return fmt.Errorf("list VEX feeds: %w", err)
	}

	if len(feeds) == 0 {
		return nil
	}

	targets, err := s.db.ListVEXTargets(ctx, "")
	if err != nil {
		return fmt.Errorf("list VEX targets: %w", err)
	}

	idx := newTargetIndex(targets)

	var errs []error

	for _, feed := range feeds {
		if err := s.applyLocked(ctx, feed, idx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func (s *Service) applyLocked(ctx context.Context, feed *models.VEXFeed, idx *targetIndex) error {
	release, acquired, err := s.lock(ctx, feed.ID)
	if err != nil || !acquired {
		return err
	}
	defer release()

	return s.applyFeed(ctx, feed, idx)
}

// applyFeed replaces the materialized statements of the feed with the matches
// of its statements in idx.
func (s *Service) applyFeed(ctx context.Context, feed *models.VEXFeed, idx *targetIndex) error {
	stmts, err := s.db.ListVEXFeedStatements(ctx, feed.ID)
	if err != nil {
		return fmt.Errorf("list statements of VEX feed %s: %w", feed.Name, err)
	}

	n, err := s.db.ReplaceFeedVEXStatements(ctx, feed.ID, idx.materialize(feed, stmts))
	if err != nil {
		return fmt.Errorf("materialize VEX feed %s: %w", feed.Name, err)
	}

	s.log.Debugw("VEX feed materialized", "feed", feed.Name, "statements", len(stmts), "applied", n)

	return nil
}

// foldID is the case-insensitive form vulnerability ids are compared in.
func foldID(id string) string { return strings.ToUpper(strings.TrimSpace(id)) }
