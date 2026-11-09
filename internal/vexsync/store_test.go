// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vexsync

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/openartifact/openartifact/internal/database/models"
)

type stmtKey struct{ repo, artifact, vuln string }

type suggestionKey struct{ peer, artifact, vuln string }

// memStore is an in-memory Store with the semantics of the database contract.
type memStore struct {
	mu sync.Mutex

	feeds     map[string]*models.VEXFeed
	feedDocs  map[string]map[string][]models.VEXFeedStatement // feed -> document URL -> statements
	syncs     []models.VEXFeedSyncResult
	targets   []models.VEXTarget
	stmts     map[stmtKey]*models.VEXStatement
	suggests  map[suggestionKey]*models.VEXSuggestion
	nextSugID int
}

func newMemStore(targets ...models.VEXTarget) *memStore {
	return &memStore{
		feeds:    map[string]*models.VEXFeed{},
		feedDocs: map[string]map[string][]models.VEXFeedStatement{},
		targets:  targets,
		stmts:    map[stmtKey]*models.VEXStatement{},
		suggests: map[suggestionKey]*models.VEXSuggestion{},
	}
}

func keyOf(s *models.VEXStatement) stmtKey { return stmtKey{s.RepositoryID, s.ArtifactID, s.VulnID} }

func (m *memStore) addFeed(f *models.VEXFeed) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.feeds[f.ID] = f
}

func (m *memStore) put(s *models.VEXStatement) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stmts[keyOf(s)] = s
}

func (m *memStore) statement(repo, artifact, vuln string) *models.VEXStatement {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.stmts[stmtKey{repo, artifact, vuln}]
}

func (m *memStore) statementCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.stmts)
}

func (m *memStore) suggestions() []*models.VEXSuggestion {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*models.VEXSuggestion, 0, len(m.suggests))
	for _, s := range m.suggests {
		out = append(out, s)
	}

	return out
}

func (m *memStore) ListVEXFeeds(context.Context) ([]*models.VEXFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*models.VEXFeed, 0, len(m.feeds))
	for _, f := range m.feeds {
		out = append(out, f)
	}

	return out, nil
}

func (m *memStore) GetVEXFeed(_ context.Context, id string) (*models.VEXFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	f, ok := m.feeds[id]
	if !ok {
		return nil, fmt.Errorf("feed %s not found", id)
	}

	return f, nil
}

func (m *memStore) ReplaceVEXFeedDocument(_ context.Context, feedID, documentURL string, stmts []models.VEXFeedStatement) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.feedDocs[feedID] == nil {
		m.feedDocs[feedID] = map[string][]models.VEXFeedStatement{}
	}

	m.feedDocs[feedID][documentURL] = slices.Clone(stmts)

	return nil
}

func (m *memStore) ListVEXFeedStatements(_ context.Context, feedID string) ([]models.VEXFeedStatement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []models.VEXFeedStatement
	for _, stmts := range m.feedDocs[feedID] {
		out = append(out, stmts...)
	}

	return out, nil
}

func (m *memStore) RecordVEXFeedSync(_ context.Context, _ string, res models.VEXFeedSyncResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.syncs = append(m.syncs, res)

	return nil
}

func (m *memStore) ListVEXTargets(_ context.Context, repoID string) ([]models.VEXTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []models.VEXTarget
	for _, t := range m.targets {
		if repoID == "" || t.RepositoryID == repoID {
			out = append(out, t)
		}
	}

	return out, nil
}

func (m *memStore) ListVEXStatements(_ context.Context, repoID, artifactID string) ([]*models.VEXStatement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []*models.VEXStatement
	for k, s := range m.stmts {
		if k.repo == repoID && (artifactID == "" || k.artifact == artifactID) {
			out = append(out, s)
		}
	}

	return out, nil
}

// ReplaceFeedVEXStatements deletes the rows of the feed, then inserts each
// statement whose key is free (ON CONFLICT DO NOTHING).
func (m *memStore) ReplaceFeedVEXStatements(_ context.Context, feedID string, stmts []*models.VEXStatement) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for k, s := range m.stmts {
		if s.FeedID == feedID {
			delete(m.stmts, k)
		}
	}

	n := 0

	for _, s := range stmts {
		if _, taken := m.stmts[keyOf(s)]; taken {
			continue
		}

		m.stmts[keyOf(s)] = s
		n++
	}

	return n, nil
}

// ApplyPeerVEXStatement inserts, or updates only a row of the same peer.
func (m *memStore) ApplyPeerVEXStatement(_ context.Context, s *models.VEXStatement) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cur, ok := m.stmts[keyOf(s)]; ok && (cur.SourceKind != models.VEXSourcePeer || cur.PeerID != s.PeerID) {
		return false, nil
	}

	m.stmts[keyOf(s)] = s

	return true, nil
}

func (m *memStore) UpsertVEXSuggestion(_ context.Context, s *models.VEXSuggestion) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	k := suggestionKey{s.PeerID, s.ArtifactID, s.VulnID}

	cur, ok := m.suggests[k]
	if ok && cur.Status == s.Status && cur.Justification == s.Justification &&
		cur.ImpactStatement == s.ImpactStatement && cur.ActionStatement == s.ActionStatement {
		s.ID, s.State = cur.ID, cur.State

		return false, nil
	}

	if ok {
		s.ID = cur.ID
	} else {
		m.nextSugID++
		s.ID = fmt.Sprintf("sug-%d", m.nextSugID)
	}

	s.State = models.VEXSuggestionPending
	stored := *s
	m.suggests[k] = &stored

	return true, nil
}

func (m *memStore) SetVEXSuggestionState(_ context.Context, repoID, id, state, decidedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, s := range m.suggests {
		if s.ID == id && s.RepositoryID == repoID {
			s.State, s.DecidedBy = state, decidedBy

			return nil
		}
	}

	return fmt.Errorf("suggestion %s not found", id)
}

func (m *memStore) PruneVEXSuggestions(_ context.Context, peerID string, keepIDs []string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var n int64

	for k, s := range m.suggests {
		if k.peer == peerID && !slices.Contains(keepIDs, s.ID) {
			delete(m.suggests, k)
			n++
		}
	}

	return n, nil
}
