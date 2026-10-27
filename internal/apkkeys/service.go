// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apkkeys manages the RSA keys that sign the APKINDEX of Alpine
// repositories. Alpine's apk verifies an index against a public key it has
// installed under /etc/apk/keys; the key is a plain RSA key, not an OpenPGP
// one, so it lives beside, not inside, the GPG key administration.
//
// Every apk repository (hosted or group) has at most one key. It is generated
// on first use, replaced by a rotation or an import, and its private half is
// sealed with internal/crypto (AES-256-GCM under OPENARTIFACT_SECRET_KEY) and
// never handed out again. Without OPENARTIFACT_SECRET_KEY no key is stored and
// the index is served unsigned.
package apkkeys

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/apk"
	"github.com/openartifact/openartifact/pkg/logger"
)

// keyBits is the size of generated keys, the size Alpine's own keys have.
const keyBits = 4096

// ErrSecretKeyRequired is returned when a key would have to be stored but
// OPENARTIFACT_SECRET_KEY is not set.
var ErrSecretKeyRequired = errors.New("OPENARTIFACT_SECRET_KEY must be set to store apk signing keys")

var errKeyVanished = errors.New("apk signing key vanished after a concurrent insert")

// ErrInvalidKey is returned for an imported key that cannot be used.
var ErrInvalidKey = errors.New("invalid apk signing key")

// Store is the slice of the database the service uses.
type Store interface {
	GetApkSigningKey(ctx context.Context, repoID string) (*models.ApkSigningKey, error)
	CreateApkSigningKeyIfAbsent(ctx context.Context, k *models.ApkSigningKey) (bool, error)
	ReplaceApkSigningKey(ctx context.Context, k *models.ApkSigningKey) error
}

// Refresher schedules the regeneration (and so the re-signing) of a repository's index.
type Refresher interface{ NotifyStale(repoID string) }

// Info describes the signing key of a repository. It carries no secret.
type Info struct {
	KeyName     string
	Fingerprint string
	PublicKey   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Service resolves, creates and rotates the signing keys.
type Service struct {
	db        Store
	log       *logger.Logger
	refresher Refresher
}

// New builds the service.
func New(db Store, log *logger.Logger) *Service {
	return &Service{db: db, log: log}
}

// SetRefresher wires what re-signs a repository after its key changed.
func (s *Service) SetRefresher(r Refresher) { s.refresher = r }

// maxKeyNameBytes is the longest key name pkg/apk signs with (the signature
// member name .SIGN.RSA256.<name> must fit a ustar header).
const maxKeyNameBytes = 87

// DefaultKeyName is the key name of a repository whose key was generated: the
// file name apk expects the public key under in /etc/apk/keys. A repository
// name too long for it is shortened and suffixed with a digest of the full
// name, so the names of different repositories stay different.
func DefaultKeyName(repoName string) string {
	const prefix, suffix = "openartifact-", ".rsa.pub"

	if name := prefix + repoName + suffix; len(name) <= maxKeyNameBytes {
		return name
	}

	sum := sha256.Sum256([]byte(repoName))
	digest := hex.EncodeToString(sum[:4])
	keep := maxKeyNameBytes - len(prefix) - len(suffix) - len(digest) - 1

	return prefix + repoName[:keep] + "-" + digest + suffix
}

// SignerForRepo returns the signer of the repository's index, generating the
// key on first use. It returns nil (and no error) when no secret key is
// configured: the index is then unsigned.
func (s *Service) SignerForRepo(ctx context.Context, repo *models.Repository) (*apk.Signer, error) {
	key, err := s.ensure(ctx, repo, "system")
	if err != nil {
		if errors.Is(err, ErrSecretKeyRequired) {
			return nil, nil //nolint:nilnil // nil signer means unsigned
		}

		return nil, err
	}

	return openSigner(key)
}

// Info returns the repository's public key description, generating the key on
// first use.
func (s *Service) Info(ctx context.Context, repo *models.Repository) (*Info, error) {
	key, err := s.ensure(ctx, repo, "system")
	if err != nil {
		return nil, err
	}

	return infoOf(key), nil
}

// Generate replaces the repository's key by a freshly generated one.
func (s *Service) Generate(ctx context.Context, repo *models.Repository, by string) (*Info, error) {
	if !crypto.Enabled() {
		return nil, ErrSecretKeyRequired
	}

	key, err := s.generate(repo, by)
	if err != nil {
		return nil, err
	}

	return s.replace(ctx, repo, key)
}

// Import replaces the repository's key by the PEM private key (PKCS#8, or the
// PKCS#1 form abuild-keygen writes). keyName "" keeps the default name.
func (s *Service) Import(ctx context.Context, repo *models.Repository, privatePEM, keyName, by string) (*Info, error) {
	if !crypto.Enabled() {
		return nil, ErrSecretKeyRequired
	}

	if err := crypto.CheckPlaintext(privatePEM); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}

	rsaKey, err := apk.ParsePrivateKeyPEM([]byte(privatePEM))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}

	if keyName == "" {
		keyName = DefaultKeyName(repo.Name)
	}

	signer, err := apk.NewSigner(keyName, rsaKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}

	key, err := seal(repo.ID, signer, rsaKey, by)
	if err != nil {
		return nil, err
	}

	return s.replace(ctx, repo, key)
}

func (s *Service) replace(ctx context.Context, repo *models.Repository, key *models.ApkSigningKey) (*Info, error) {
	if err := s.db.ReplaceApkSigningKey(ctx, key); err != nil {
		return nil, fmt.Errorf("storing apk signing key: %w", err)
	}

	if s.refresher != nil {
		s.refresher.NotifyStale(repo.ID)
	}

	return infoOf(key), nil
}

// ensure returns the stored key of the repository, creating it when absent.
// Concurrent callers (replicas) converge on one key: the insert is
// conditional and the loser reads the winner's row.
func (s *Service) ensure(ctx context.Context, repo *models.Repository, by string) (*models.ApkSigningKey, error) {
	key, err := s.db.GetApkSigningKey(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("loading apk signing key: %w", err)
	}

	if key != nil {
		return key, nil
	}

	if !crypto.Enabled() {
		return nil, ErrSecretKeyRequired
	}

	fresh, err := s.generate(repo, by)
	if err != nil {
		return nil, err
	}

	created, err := s.db.CreateApkSigningKeyIfAbsent(ctx, fresh)
	if err != nil {
		return nil, fmt.Errorf("storing apk signing key: %w", err)
	}

	if created {
		s.log.Infow("apk signing key generated", "repo_id", repo.ID, "key", fresh.KeyName)
		return fresh, nil
	}

	key, err = s.db.GetApkSigningKey(ctx, repo.ID)
	if err != nil {
		return nil, fmt.Errorf("loading apk signing key: %w", err)
	}

	if key == nil {
		return nil, errKeyVanished
	}

	return key, nil
}

func (s *Service) generate(repo *models.Repository, by string) (*models.ApkSigningKey, error) {
	rsaKey, err := apk.GenerateKey(keyBits)
	if err != nil {
		return nil, fmt.Errorf("generating apk signing key: %w", err)
	}

	signer, err := apk.NewSigner(DefaultKeyName(repo.Name), rsaKey)
	if err != nil {
		return nil, fmt.Errorf("building apk signer: %w", err)
	}

	return seal(repo.ID, signer, rsaKey, by)
}

// seal builds the stored record of a signer: public half in clear, private
// half encrypted.
func seal(repoID string, signer *apk.Signer, key *rsa.PrivateKey, by string) (*models.ApkSigningKey, error) {
	pub, err := signer.PublicKeyPEM()
	if err != nil {
		return nil, fmt.Errorf("exporting apk public key: %w", err)
	}

	sealed, err := crypto.Encrypt(string(apk.MarshalPrivateKeyPEM(key)))
	if err != nil {
		return nil, fmt.Errorf("sealing apk private key: %w", err)
	}

	if !crypto.IsEncrypted(sealed) {
		return nil, ErrSecretKeyRequired
	}

	now := time.Now().UTC()

	return &models.ApkSigningKey{
		RepositoryID:  repoID,
		KeyName:       signer.KeyName(),
		Fingerprint:   signer.Fingerprint(),
		PublicKeyPEM:  strings.TrimSpace(string(pub)) + "\n",
		PrivateKeyEnc: []byte(sealed),
		CreatedBy:     by,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func openSigner(key *models.ApkSigningKey) (*apk.Signer, error) {
	plain, err := crypto.Decrypt(string(key.PrivateKeyEnc))
	if err != nil {
		return nil, fmt.Errorf("decrypting apk signing key of repository %s: %w", key.RepositoryID, err)
	}

	rsaKey, err := apk.ParsePrivateKeyPEM([]byte(plain))
	if err != nil {
		return nil, fmt.Errorf("loading apk signing key of repository %s: %w", key.RepositoryID, err)
	}

	signer, err := apk.NewSigner(key.KeyName, rsaKey)
	if err != nil {
		return nil, fmt.Errorf("building apk signer of repository %s: %w", key.RepositoryID, err)
	}

	return signer, nil
}

func infoOf(key *models.ApkSigningKey) *Info {
	return &Info{
		KeyName:     key.KeyName,
		Fingerprint: key.Fingerprint,
		PublicKey:   key.PublicKeyPEM,
		CreatedAt:   key.CreatedAt,
		UpdatedAt:   key.UpdatedAt,
	}
}
