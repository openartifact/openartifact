// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// GPG key sources: how a database-managed signing key came to exist.
const (
	GPGKeySourceImported  = "imported"
	GPGKeySourceGenerated = "generated"
	GPGKeySourceAuto      = "auto"
)

// GPGKey is a database-managed GPG signing key. PrivateKeyEnc is the armored,
// passphrase-less private key sealed by internal/crypto; it is only filled by
// the single-key getters and the repository resolver, never by ListGPGKeys,
// and is never serialised into an API response.
type GPGKey struct {
	ID             string
	Name           string
	Fingerprint    string
	KeyID          string
	Algorithm      string
	UserID         string
	PublicKey      string
	PrivateKeyEnc  string
	IsGlobalActive bool
	Source         string
	CreatedBy      string
	CreatedAt      time.Time
	ExpiresAt      *time.Time
}

// GPGKeyAssignment binds a repository to the key that signs its metadata.
type GPGKeyAssignment struct {
	KeyID            string
	RepositoryID     string
	RepositoryName   string
	RepositoryFormat string
}

// GPGSigningRepo identifies a repository whose metadata a key signs.
type GPGSigningRepo struct {
	ID     string
	Format string
}
