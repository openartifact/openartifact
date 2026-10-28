// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nixkeys manages the ed25519 keys that sign the narinfo of Nix binary
// caches. A Nix client accepts a store path from a cache when one of its
// signatures verifies under a key in trusted-public-keys; the key is a plain
// ed25519 key in Nix's own text format, so it lives beside, not inside, the
// GPG key administration.
//
// Every hosted or group nix repository has at most one key. It is generated on first use,
// replaced by a rotation or an import, and its private half is sealed with
// internal/crypto (AES-256-GCM under OPENARTIFACT_SECRET_KEY) and never handed
// out again. Without OPENARTIFACT_SECRET_KEY no key is stored and narinfo are
// served without the repository's signature.
//
// Signatures are made when a narinfo is served, from the stored key, so a
// rotation re-signs every path at once and no signature is ever stored.
package nixkeys

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/pkg/nix"
)

// ErrSecretKeyRequired is returned when a key would have to be stored but
// OPENARTIFACT_SECRET_KEY is not set.
var ErrSecretKeyRequired = errors.New("OPENARTIFACT_SECRET_KEY must be set to store nix signing keys")

var errKeyVanished = errors.New("nix signing key vanished after a concurrent insert")

// ErrInvalidKey is returned for an imported key that cannot be used.
var ErrInvalidKey = errors.New("invalid nix signing key")

// Store is the slice of the database the service uses.
type Store interface {
	GetNixSigningKey(ctx context.Context, repoID string) (*models.NixSigningKey, error)
	CreateNixSigningKeyIfAbsent(ctx context.Context, k *models.NixSigningKey) (bool, error)
	ReplaceNixSigningKey(ctx context.Context, k *models.NixSigningKey) error
}

// Info describes the signing key of a repository. It carries no secret.
type Info struct {
	// KeyName is the name part of the key, which signatures carry.
	KeyName string
	// PublicKey is the "<name>:<base64>" line for trusted-public-keys.
	PublicKey string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// opened is a decrypted key together with the sealed record it came from.
type opened struct {
	sealed string
	key    *nix.SecretKey
}

// Service resolves, creates and rotates the signing keys.
type Service struct {
	db  Store
	log *logger.Logger

	// cache holds the decrypted key of a repository as long as its stored
	// record is unchanged, so serving a narinfo parses no key.
	mu    sync.Mutex
	cache map[string]opened
}

// New builds the service.
func New(db Store, log *logger.Logger) *Service {
	return &Service{db: db, log: log, cache: map[string]opened{}}
}

// DefaultKeyName is the name of the n-th key generated for a repository.
// Every rotation takes a new name: Nix keeps one public key per name, so a
// client could not trust the old and the new key side by side under one.
func DefaultKeyName(repoName string, n int) string {
	return "openartifact-" + repoName + "-" + strconv.Itoa(n)
}

// SecretKeyForRepo returns the key that signs the repository's narinfo,
// generating it on first use. It returns nil (and no error) when no secret key
// is configured: narinfo are then served without the repository's signature.
func (s *Service) SecretKeyForRepo(ctx context.Context, repo *models.Repository) (*nix.SecretKey, error) {
	rec, err := s.ensure(ctx, repo, "system")
	if err != nil {
		if errors.Is(err, ErrSecretKeyRequired) {
			return nil, nil //nolint:nilnil // nil key means unsigned
		}

		return nil, err
	}

	return s.open(rec)
}

// Info returns the repository's public key description, generating the key on
// first use.
func (s *Service) Info(ctx context.Context, repo *models.Repository) (*Info, error) {
	rec, err := s.ensure(ctx, repo, "system")
	if err != nil {
		return nil, err
	}

	return infoOf(rec), nil
}

// Generate replaces the repository's key by a freshly generated one, named
// after the previous one's generation number.
func (s *Service) Generate(ctx context.Context, repo *models.Repository, by string) (*Info, error) {
	if !crypto.Enabled() {
		return nil, ErrSecretKeyRequired
	}

	old, err := s.db.GetNixSigningKey(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("loading nix signing key: %w", err)
	}

	key, err := nix.GenerateKey(nextName(repo.Name, old))
	if err != nil {
		return nil, fmt.Errorf("generating nix signing key: %w", err)
	}

	return s.replace(ctx, repo, key, by)
}

// Import replaces the repository's key by a secret key in the format of
// "nix-store --generate-binary-cache-key", "<name>:<base64>". The name of the
// imported key is kept; keyName "" keeps it as it is, anything else renames it.
func (s *Service) Import(ctx context.Context, repo *models.Repository, secret, keyName, by string) (*Info, error) {
	if !crypto.Enabled() {
		return nil, ErrSecretKeyRequired
	}

	if err := crypto.CheckPlaintext(secret); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}

	key, err := nix.ParseSecretKey(secret)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}

	if keyName != "" && keyName != key.Name() {
		if key, err = key.Renamed(keyName); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
		}
	}

	return s.replace(ctx, repo, key, by)
}

func (s *Service) replace(ctx context.Context, repo *models.Repository, key *nix.SecretKey, by string) (*Info, error) {
	rec, err := seal(repo.ID, key, by)
	if err != nil {
		return nil, err
	}

	if err := s.db.ReplaceNixSigningKey(ctx, rec); err != nil {
		return nil, fmt.Errorf("storing nix signing key: %w", err)
	}

	return infoOf(rec), nil
}

// ensure returns the stored key of the repository, creating it when absent.
// Concurrent callers (replicas) converge on one key: the insert is
// conditional and the loser reads the winner's row.
func (s *Service) ensure(ctx context.Context, repo *models.Repository, by string) (*models.NixSigningKey, error) {
	rec, err := s.db.GetNixSigningKey(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("loading nix signing key: %w", err)
	}

	if rec != nil {
		return rec, nil
	}

	if !crypto.Enabled() {
		return nil, ErrSecretKeyRequired
	}

	key, err := nix.GenerateKey(DefaultKeyName(repo.Name, 1))
	if err != nil {
		return nil, fmt.Errorf("generating nix signing key: %w", err)
	}

	fresh, err := seal(repo.ID, key, by)
	if err != nil {
		return nil, err
	}

	created, err := s.db.CreateNixSigningKeyIfAbsent(ctx, fresh)
	if err != nil {
		return nil, fmt.Errorf("storing nix signing key: %w", err)
	}

	if created {
		s.log.Infow("nix signing key generated", "repo_id", repo.ID, "key", fresh.KeyName)
		return fresh, nil
	}

	rec, err = s.db.GetNixSigningKey(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("loading nix signing key: %w", err)
	}

	if rec == nil {
		return nil, errKeyVanished
	}

	return rec, nil
}

// open decrypts the stored key, or returns the one decrypted from the same
// sealed record before.
func (s *Service) open(rec *models.NixSigningKey) (*nix.SecretKey, error) {
	sealed := string(rec.PrivateKeyEnc)

	s.mu.Lock()
	hit, ok := s.cache[rec.RepositoryID]
	s.mu.Unlock()

	if ok && hit.sealed == sealed {
		return hit.key, nil
	}

	plain, err := crypto.Decrypt(sealed)
	if err != nil {
		return nil, fmt.Errorf("decrypting nix signing key of repository %s: %w", rec.RepositoryID, err)
	}

	key, err := nix.ParseSecretKey(plain)
	if err != nil {
		return nil, fmt.Errorf("loading nix signing key of repository %s: %w", rec.RepositoryID, err)
	}

	s.mu.Lock()
	s.cache[rec.RepositoryID] = opened{sealed: sealed, key: key}
	s.mu.Unlock()

	return key, nil
}

// nextName names the key that replaces old: generation n+1 when old is named
// like a generated key, generation 1 otherwise.
func nextName(repoName string, old *models.NixSigningKey) string {
	n := 1

	if old != nil {
		if i := strings.LastIndexByte(old.KeyName, '-'); i >= 0 {
			if gen, err := strconv.Atoi(old.KeyName[i+1:]); err == nil && gen >= 1 {
				n = gen + 1
			}
		}
	}

	return DefaultKeyName(repoName, n)
}

// seal builds the stored record of a key: public half in clear, private half
// encrypted.
func seal(repoID string, key *nix.SecretKey, by string) (*models.NixSigningKey, error) {
	sealed, err := crypto.Encrypt(key.String())
	if err != nil {
		return nil, fmt.Errorf("sealing nix private key: %w", err)
	}

	if !crypto.IsEncrypted(sealed) {
		return nil, ErrSecretKeyRequired
	}

	now := time.Now().UTC()

	return &models.NixSigningKey{
		RepositoryID:  repoID,
		KeyName:       key.Name(),
		PublicKey:     key.PublicKey(),
		PrivateKeyEnc: []byte(sealed),
		CreatedBy:     by,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func infoOf(rec *models.NixSigningKey) *Info {
	return &Info{KeyName: rec.KeyName, PublicKey: rec.PublicKey, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt}
}
