// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gpgkeystest provides an in-memory gpgkeys.Store for tests. It keeps
// the semantics the PostgreSQL implementation guarantees: unique name and
// fingerprint, at most one globally active key, and a CreateGPGKeyIfNone whose
// check and insert are atomic (the database holds an advisory lock there).
package gpgkeystest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

// Store is an in-memory gpgkeys.Store.
type Store struct {
	mu          sync.Mutex
	next        int
	keys        map[string]*models.GPGKey
	assignments map[string]string // repository id -> key id
	repos       map[string]models.GPGSigningRepo
	repoNames   map[string]string
	stale       []string
}

// New returns an empty store.
func New() *Store {
	return &Store{
		keys:        map[string]*models.GPGKey{},
		assignments: map[string]string{},
		repos:       map[string]models.GPGSigningRepo{},
		repoNames:   map[string]string{},
	}
}

// AddRepository registers a hosted deb/rpm repository the store knows about.
func (s *Store) AddRepository(id, name, format string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.repos[id] = models.GPGSigningRepo{ID: id, Format: format}
	s.repoNames[id] = name
}

// StaleRepositories returns the ids marked stale so far, in order.
func (s *Store) StaleRepositories() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.stale...)
}

// Raw returns a copy of the stored record including PrivateKeyEnc, the way the
// database holds it.
func (s *Store) Raw(id string) (models.GPGKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, ok := s.keys[id]
	if !ok {
		return models.GPGKey{}, false
	}

	return *k, true
}

// Count is the number of stored keys.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.keys)
}

func (s *Store) insertLocked(k *models.GPGKey) error {
	for _, e := range s.keys {
		if e.Name == k.Name || e.Fingerprint == k.Fingerprint {
			return database.ErrGPGKeyExists
		}
	}

	s.next++
	k.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", s.next)
	k.CreatedAt = time.Now()
	k.IsGlobalActive = false

	cp := *k
	s.keys[k.ID] = &cp

	return nil
}

func (s *Store) CreateGPGKey(_ context.Context, k *models.GPGKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.insertLocked(k)
}

func (s *Store) CreateGPGKeyIfNone(_ context.Context, k *models.GPGKey) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.keys) > 0 {
		return false, nil
	}

	if err := s.insertLocked(k); err != nil {
		return false, err
	}

	s.keys[k.ID].IsGlobalActive = true
	k.IsGlobalActive = true

	return true, nil
}

func (s *Store) ListGPGKeys(_ context.Context) ([]*models.GPGKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]*models.GPGKey, 0, len(s.keys))

	for _, k := range s.keys {
		cp := *k
		cp.PrivateKeyEnc = "" // the database list never selects it
		out = append(out, &cp)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out, nil
}

func (s *Store) GetGPGKey(_ context.Context, id string) (*models.GPGKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, ok := s.keys[id]
	if !ok {
		return nil, database.ErrGPGKeyNotFound
	}

	cp := *k

	return &cp, nil
}

func (s *Store) GetGPGKeyByFingerprint(_ context.Context, fingerprint string) (*models.GPGKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, k := range s.keys {
		if k.Fingerprint == fingerprint {
			cp := *k
			return &cp, nil
		}
	}

	return nil, database.ErrGPGKeyNotFound
}

func (s *Store) DeleteGPGKey(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.keys[id]; !ok {
		return database.ErrGPGKeyNotFound
	}

	delete(s.keys, id)

	for repo, key := range s.assignments {
		if key == id {
			delete(s.assignments, repo)
		}
	}

	return nil
}

func (s *Store) SetGlobalGPGKey(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.keys[id]; !ok {
		return database.ErrGPGKeyNotFound
	}

	for _, k := range s.keys {
		k.IsGlobalActive = k.ID == id
	}

	return nil
}

func (s *Store) ClearGlobalGPGKey(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, k := range s.keys {
		k.IsGlobalActive = false
	}

	return nil
}

func (s *Store) SetRepositoryGPGKey(_ context.Context, repoID, keyID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.keys[keyID]; !ok {
		return database.ErrGPGKeyNotFound
	}

	s.assignments[repoID] = keyID

	return nil
}

func (s *Store) ClearRepositoryGPGKey(_ context.Context, repoID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.assignments[repoID]
	delete(s.assignments, repoID)

	return ok, nil
}

func (s *Store) ListGPGKeyAssignments(_ context.Context) ([]models.GPGKeyAssignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []models.GPGKeyAssignment

	for repo, key := range s.assignments {
		out = append(out, models.GPGKeyAssignment{
			KeyID: key, RepositoryID: repo, RepositoryName: s.repoNames[repo], RepositoryFormat: s.repos[repo].Format,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].RepositoryName < out[j].RepositoryName })

	return out, nil
}

func (s *Store) ListRepositoriesForGPGKey(_ context.Context, keyID string) ([]models.GPGSigningRepo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []models.GPGSigningRepo

	for repo, key := range s.assignments {
		if key == keyID {
			out = append(out, models.GPGSigningRepo{ID: repo, Format: s.repos[repo].Format})
		}
	}

	return out, nil
}

func (s *Store) ListSigningRepositoriesWithoutKey(_ context.Context) ([]models.GPGSigningRepo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []models.GPGSigningRepo

	for id, r := range s.repos {
		if _, own := s.assignments[id]; !own {
			out = append(out, r)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	return out, nil
}

func (s *Store) ResolveGPGKeyForRepository(_ context.Context, repoID string) (*models.GPGKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.assignments[repoID]; ok {
		cp := *s.keys[id]
		return &cp, nil
	}

	for _, k := range s.keys {
		if k.IsGlobalActive {
			cp := *k
			return &cp, nil
		}
	}

	return nil, nil //nolint:nilnil // "no key" is not an error
}

func (s *Store) MarkRepoStale(_ context.Context, repositoryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stale = append(s.stale, repositoryID)

	return nil
}
