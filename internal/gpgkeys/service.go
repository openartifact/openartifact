// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gpgkeys manages the GPG keys that sign deb/rpm repository metadata
// and sealed release bundles: import, generation, activation (globally or per
// repository), deletion and the lookup the metadata workers use to find the
// signer of a repository.
//
// Private keys are stored as unprotected armored blocks sealed with
// internal/crypto (AES-256-GCM under OPENARTIFACT_SECRET_KEY) and are never
// handed out again. The key configured through gpg.keyPath / GPG_KEY_PATH keeps
// working as a read-only fallback that applies wherever no database key does.
package gpgkeys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ConfigKeyID is the id of the read-only entry for the file/env-configured key.
const ConfigKeyID = "config"

// SourceConfig is the Source of the file/env-configured key.
const SourceConfig = "config"

// Errors returned by the Service.
var (
	ErrSecretKeyRequired = errors.New("OPENARTIFACT_SECRET_KEY must be set to store GPG private keys")
	ErrKeyInUse          = errors.New("GPG key is active; deactivate it first or force the deletion")
	ErrReadOnly          = errors.New("the key configured through gpg.keyPath is read-only")
	ErrNotActive         = errors.New("GPG key is not active there")
	ErrNotFound          = database.ErrGPGKeyNotFound
	ErrExists            = database.ErrGPGKeyExists
)

// Store is the slice of the database the service uses.
type Store interface {
	CreateGPGKey(ctx context.Context, k *models.GPGKey) error
	CreateGPGKeyIfNone(ctx context.Context, k *models.GPGKey) (bool, error)
	ListGPGKeys(ctx context.Context) ([]*models.GPGKey, error)
	GetGPGKey(ctx context.Context, id string) (*models.GPGKey, error)
	GetGPGKeyByFingerprint(ctx context.Context, fingerprint string) (*models.GPGKey, error)
	DeleteGPGKey(ctx context.Context, id string) error
	SetGlobalGPGKey(ctx context.Context, id string) error
	ClearGlobalGPGKey(ctx context.Context) error
	SetRepositoryGPGKey(ctx context.Context, repoID, keyID, assignedBy string) error
	ClearRepositoryGPGKey(ctx context.Context, repoID string) (bool, error)
	ListGPGKeyAssignments(ctx context.Context) ([]models.GPGKeyAssignment, error)
	ListRepositoriesForGPGKey(ctx context.Context, keyID string) ([]models.GPGSigningRepo, error)
	ListSigningRepositoriesWithoutKey(ctx context.Context) ([]models.GPGSigningRepo, error)
	ResolveGPGKeyForRepository(ctx context.Context, repoID string) (*models.GPGKey, error)
	MarkRepoStale(ctx context.Context, repositoryID string) error
}

// Refresher schedules the regeneration (and so the re-signing) of a repository's metadata.
type Refresher interface{ NotifyStale(repoID string) }

// Service implements gpg.Resolver on top of the database-managed keys.
type Service struct {
	db     Store
	log    *logger.Logger
	config gpg.Signer
	rpm    Refresher
	deb    Refresher

	// autoAlgorithm is the type of the key AutoGenerate creates.
	autoAlgorithm gpg.Algorithm
}

// New builds the service. config is the file/env key (nil when none is configured).
func New(db Store, log *logger.Logger, config gpg.Signer) *Service {
	return &Service{db: db, log: log, config: config, autoAlgorithm: gpg.AlgorithmRSA4096}
}

// SetRefreshers wires the rpm and deb metadata workers; either may be nil.
func (s *Service) SetRefreshers(rpm, deb Refresher) {
	s.rpm, s.deb = rpm, deb
}

// ConfigSigner returns the file/env-configured signer, or nil.
func (s *Service) ConfigSigner() gpg.Signer { return s.config }

// SignerForRepo implements gpg.Resolver: the repository's own key, else the
// global active key, else the file/env key; nil when nothing signs it.
func (s *Service) SignerForRepo(ctx context.Context, repoID string) (gpg.Signer, error) {
	key, err := s.db.ResolveGPGKeyForRepository(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("resolving signing key: %w", err)
	}

	if key == nil {
		return s.config, nil
	}

	return openSigner(key)
}

// SignerByID loads the signer of one stored key (or the config key for ConfigKeyID).
func (s *Service) SignerByID(ctx context.Context, id string) (gpg.Signer, *models.GPGKey, error) {
	if id == ConfigKeyID {
		if s.config == nil {
			return nil, nil, ErrNotFound
		}

		return s.config, nil, nil
	}

	key, err := s.db.GetGPGKey(ctx, id)
	if err != nil {
		return nil, nil, err //nolint:wrapcheck // sentinel passed through
	}

	signer, err := openSigner(key)
	if err != nil {
		return nil, nil, err
	}

	return signer, key, nil
}

// SignerByRef resolves a key reference given as a stored key id, a fingerprint
// (any case, with or without spaces and 0x prefix) or ConfigKeyID.
func (s *Service) SignerByRef(ctx context.Context, ref string) (gpg.Signer, *models.GPGKey, error) {
	ref = strings.TrimSpace(ref)
	if ref == ConfigKeyID {
		return s.SignerByID(ctx, ref)
	}

	if fp := normalizeFingerprint(ref); fp != "" {
		if s.config != nil && strings.EqualFold(s.config.KeyFingerprint(), fp) {
			return s.config, nil, nil
		}

		if key, err := s.db.GetGPGKeyByFingerprint(ctx, fp); err == nil {
			signer, err := openSigner(key)

			return signer, key, err
		} else if !errors.Is(err, ErrNotFound) {
			return nil, nil, err //nolint:wrapcheck // sentinel passed through
		}
	}

	if !isUUID(ref) {
		return nil, nil, ErrNotFound
	}

	return s.SignerByID(ctx, ref)
}

func openSigner(key *models.GPGKey) (gpg.Signer, error) {
	plain, err := crypto.Decrypt(key.PrivateKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypting GPG key %s: %w", key.ID, err)
	}

	signer, err := gpg.ParsePrivateKey(strings.NewReader(plain), nil)
	if err != nil {
		return nil, fmt.Errorf("loading GPG key %s: %w", key.ID, err)
	}

	return signer, nil
}

func normalizeFingerprint(ref string) string {
	ref = strings.ToUpper(strings.NewReplacer(" ", "", "0X", "").Replace(strings.ToUpper(ref)))
	if len(ref) < 32 || len(ref) > 64 {
		return ""
	}

	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'A' || c > 'F') {
			return ""
		}
	}

	return ref
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}

	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F'):
			return false
		}
	}

	return true
}

// ImportInput describes an armored private key to import.
type ImportInput struct {
	Name       string
	PrivateKey string
	Passphrase string
	CreatedBy  string
}

// Import stores an armored private key, unlocking it with the passphrase when
// it is protected. The key is stored inactive.
func (s *Service) Import(ctx context.Context, in ImportInput) (*models.GPGKey, error) {
	if !crypto.Enabled() {
		return nil, ErrSecretKeyRequired
	}

	if err := crypto.CheckPlaintext(in.PrivateKey); err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}

	signer, err := gpg.ParsePrivateKey(strings.NewReader(in.PrivateKey), []byte(in.Passphrase))
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}

	return s.store(ctx, signer, in.Name, models.GPGKeySourceImported, in.CreatedBy)
}

// GenerateInput describes a key to generate.
type GenerateInput struct {
	Name      string
	Algorithm gpg.Algorithm
	UserName  string
	Comment   string
	Email     string
	CreatedBy string
}

// Generate creates a new key pair and stores it inactive.
func (s *Service) Generate(ctx context.Context, in GenerateInput) (*models.GPGKey, error) {
	if !crypto.Enabled() {
		return nil, ErrSecretKeyRequired
	}

	signer, err := newSigner(in)
	if err != nil {
		return nil, err
	}

	return s.store(ctx, signer, in.Name, models.GPGKeySourceGenerated, in.CreatedBy)
}

func newSigner(in GenerateInput) (*gpg.LocalSigner, error) {
	userName := in.UserName
	if userName == "" {
		userName = "OpenArtifact repository signing key"
	}

	entity, err := gpg.GenerateEntity(userName, in.Comment, in.Email, in.Algorithm)
	if err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}

	return gpg.NewLocalSignerFromEntity(entity) //nolint:wrapcheck // typed gpg errors
}

func (s *Service) newKey(signer *gpg.LocalSigner, name, source, createdBy string) (*models.GPGKey, error) {
	priv, err := signer.ExportUnprotectedPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("exporting private key: %w", err)
	}

	sealed, err := crypto.Encrypt(string(priv))
	if err != nil {
		return nil, fmt.Errorf("sealing private key: %w", err)
	}

	if !crypto.IsEncrypted(sealed) {
		return nil, ErrSecretKeyRequired
	}

	pub, err := signer.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("exporting public key: %w", err)
	}

	info := signer.Info()

	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultName(info)
	}

	return &models.GPGKey{
		Name:          name,
		Fingerprint:   info.Fingerprint,
		KeyID:         info.KeyID,
		Algorithm:     info.Algorithm,
		UserID:        info.UserID,
		PublicKey:     string(bytes.TrimSpace(pub)) + "\n",
		PrivateKeyEnc: sealed,
		Source:        source,
		CreatedBy:     createdBy,
		ExpiresAt:     info.ExpiresAt,
	}, nil
}

func defaultName(info gpg.KeyInfo) string {
	return "key-" + strings.ToLower(info.KeyID)
}

func (s *Service) store(ctx context.Context, signer *gpg.LocalSigner, name, source, createdBy string) (*models.GPGKey, error) {
	key, err := s.newKey(signer, name, source, createdBy)
	if err != nil {
		return nil, err
	}

	if err := s.db.CreateGPGKey(ctx, key); err != nil {
		return nil, err //nolint:wrapcheck // sentinel passed through
	}

	return key, nil
}

// AutoGenerate creates a global active RSA-4096 key when no key exists at all
// (neither in the database nor through gpg.keyPath). Replicas that start
// together create exactly one key: the check and the insert happen under a
// database advisory lock. It reports whether this call created the key.
func (s *Service) AutoGenerate(ctx context.Context) (bool, error) {
	if s.config != nil {
		return false, nil
	}

	if !crypto.Enabled() {
		return false, ErrSecretKeyRequired
	}

	// Cheap pre-check so a restart does not pay for a 4096-bit key generation.
	existing, err := s.db.ListGPGKeys(ctx)
	if err != nil {
		return false, fmt.Errorf("listing GPG keys: %w", err)
	}

	if len(existing) > 0 {
		return false, nil
	}

	signer, err := newSigner(GenerateInput{Algorithm: s.autoAlgorithm, UserName: "OpenArtifact repository signing key"})
	if err != nil {
		return false, err
	}

	key, err := s.newKey(signer, "auto-generated", models.GPGKeySourceAuto, "system")
	if err != nil {
		return false, err
	}

	created, err := s.db.CreateGPGKeyIfNone(ctx, key)
	if err != nil {
		return false, fmt.Errorf("storing auto-generated GPG key: %w", err)
	}

	if created {
		s.refreshFollowers(ctx)
	}

	return created, nil
}

// Delete removes a key. An active key (global or assigned to repositories) is
// refused with ErrKeyInUse unless force is set. The repositories it signed are
// re-rendered, so they fall back to the next key or become unsigned.
func (s *Service) Delete(ctx context.Context, id string, force bool) error {
	if id == ConfigKeyID {
		return ErrReadOnly
	}

	key, err := s.db.GetGPGKey(ctx, id)
	if err != nil {
		return err //nolint:wrapcheck // sentinel passed through
	}

	assigned, err := s.db.ListRepositoriesForGPGKey(ctx, id)
	if err != nil {
		return fmt.Errorf("listing repositories of GPG key: %w", err)
	}

	if (key.IsGlobalActive || len(assigned) > 0) && !force {
		return ErrKeyInUse
	}

	var followers []models.GPGSigningRepo
	if key.IsGlobalActive {
		if followers, err = s.db.ListSigningRepositoriesWithoutKey(ctx); err != nil {
			return fmt.Errorf("listing repositories following the global key: %w", err)
		}
	}

	if err := s.db.DeleteGPGKey(ctx, id); err != nil {
		return err //nolint:wrapcheck // sentinel passed through
	}

	s.refresh(ctx, append(followers, assigned...))

	return nil
}

// Activate makes the key sign one repository (repo != nil, a deb or rpm repo)
// or, otherwise, every repository without a key of its own.
func (s *Service) Activate(ctx context.Context, id string, repo *models.GPGSigningRepo, by string) error {
	if id == ConfigKeyID {
		return ErrReadOnly
	}

	if _, err := s.db.GetGPGKey(ctx, id); err != nil {
		return err //nolint:wrapcheck // sentinel passed through
	}

	if repo != nil {
		if err := s.db.SetRepositoryGPGKey(ctx, repo.ID, id, by); err != nil {
			return err //nolint:wrapcheck // already wrapped
		}

		s.refresh(ctx, []models.GPGSigningRepo{*repo})

		return nil
	}

	if err := s.db.SetGlobalGPGKey(ctx, id); err != nil {
		return err //nolint:wrapcheck // sentinel passed through
	}

	s.refreshFollowers(ctx)

	return nil
}

// Deactivate stops the key signing one repository (repo != nil) or globally.
// ErrNotActive when the key was not the signer there.
func (s *Service) Deactivate(ctx context.Context, id string, repo *models.GPGSigningRepo) error {
	if id == ConfigKeyID {
		return ErrReadOnly
	}

	if repo != nil {
		assigned, err := s.db.ListRepositoriesForGPGKey(ctx, id)
		if err != nil {
			return fmt.Errorf("listing repositories of GPG key: %w", err)
		}

		if !hasRepo(assigned, repo.ID) {
			return ErrNotActive
		}

		if _, err := s.db.ClearRepositoryGPGKey(ctx, repo.ID); err != nil {
			return err //nolint:wrapcheck // already wrapped
		}

		s.refresh(ctx, []models.GPGSigningRepo{*repo})

		return nil
	}

	key, err := s.db.GetGPGKey(ctx, id)
	if err != nil {
		return err //nolint:wrapcheck // sentinel passed through
	}

	if !key.IsGlobalActive {
		return ErrNotActive
	}

	if err := s.db.ClearGlobalGPGKey(ctx); err != nil {
		return err //nolint:wrapcheck // already wrapped
	}

	s.refreshFollowers(ctx)

	return nil
}

func hasRepo(repos []models.GPGSigningRepo, id string) bool {
	for _, r := range repos {
		if r.ID == id {
			return true
		}
	}

	return false
}

// PublicKey returns the armored public key of a stored key or of the config key.
func (s *Service) PublicKey(ctx context.Context, id string) ([]byte, string, error) {
	if id == ConfigKeyID {
		if s.config == nil {
			return nil, "", ErrNotFound
		}

		pub, err := s.config.PublicKey()

		return pub, "config", err //nolint:wrapcheck // typed gpg errors
	}

	key, err := s.db.GetGPGKey(ctx, id)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // sentinel passed through
	}

	return []byte(key.PublicKey), key.Name, nil
}

// Entry is one row of the key list: a stored key or the read-only config key.
type Entry struct {
	ID           string
	Name         string
	Fingerprint  string
	KeyID        string
	Algorithm    string
	UserID       string
	Source       string
	ReadOnly     bool
	ActiveGlobal bool
	Repositories []string
	CreatedBy    string
	CreatedAt    time.Time
	ExpiresAt    *time.Time
}

// List returns the stored keys followed by the config key, if configured.
// ActiveGlobal on the config key means "no database key is globally active, so
// it signs every repository that has no key of its own".
func (s *Service) List(ctx context.Context) ([]Entry, error) {
	keys, err := s.db.ListGPGKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing GPG keys: %w", err)
	}

	assignments, err := s.db.ListGPGKeyAssignments(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing GPG key assignments: %w", err)
	}

	repos := make(map[string][]string, len(keys))
	for _, a := range assignments {
		repos[a.KeyID] = append(repos[a.KeyID], a.RepositoryName)
	}

	out := make([]Entry, 0, len(keys)+1)
	globalActive := false

	for _, k := range keys {
		globalActive = globalActive || k.IsGlobalActive
		r := repos[k.ID]

		if r == nil {
			r = []string{}
		}

		out = append(out, Entry{
			ID: k.ID, Name: k.Name, Fingerprint: k.Fingerprint, KeyID: k.KeyID, Algorithm: k.Algorithm,
			UserID: k.UserID, Source: k.Source, ActiveGlobal: k.IsGlobalActive, Repositories: r,
			CreatedBy: k.CreatedBy, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt,
		})
	}

	if ls, ok := s.config.(interface{ Info() gpg.KeyInfo }); ok {
		info := ls.Info()
		out = append(out, Entry{
			ID: ConfigKeyID, Name: "config", Fingerprint: info.Fingerprint, KeyID: info.KeyID, Algorithm: info.Algorithm,
			UserID: info.UserID, Source: SourceConfig, ReadOnly: true, ActiveGlobal: !globalActive,
			Repositories: []string{}, CreatedBy: "", ExpiresAt: info.ExpiresAt,
		})
	}

	return out, nil
}

func (s *Service) refreshFollowers(ctx context.Context) {
	repos, err := s.db.ListSigningRepositoriesWithoutKey(ctx)
	if err != nil {
		s.log.Warnw("gpg: listing repositories to re-sign failed", "error", err)
		return
	}

	s.refresh(ctx, repos)
}

// refresh re-renders the metadata of the repositories so their signatures
// follow the new key assignment.
func (s *Service) refresh(ctx context.Context, repos []models.GPGSigningRepo) {
	seen := make(map[string]struct{}, len(repos))

	for _, r := range repos {
		if _, dup := seen[r.ID]; dup {
			continue
		}

		seen[r.ID] = struct{}{}

		switch r.Format {
		case "rpm":
			s.refreshRPM(ctx, r.ID)
		case "deb":
			if s.deb != nil {
				s.deb.NotifyStale(r.ID)
			}
		}
	}
}

func (s *Service) refreshRPM(ctx context.Context, repoID string) {
	if s.rpm == nil {
		return
	}

	if err := s.db.MarkRepoStale(ctx, repoID); err != nil {
		s.log.Warnw("gpg: marking rpm repository stale failed; it is re-signed on its next change", "repository_id", repoID, "error", err)
		return
	}

	s.rpm.NotifyStale(repoID)
}
