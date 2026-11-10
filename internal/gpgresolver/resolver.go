// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gpgresolver holds the contract between the code that signs
// repository metadata and the service that knows which key signs which
// repository.
package gpgresolver

import (
	"context"

	"github.com/M0Rf30/pkgformat/gpg"
)

// Resolver picks the signer for a repository. A nil Signer with a nil error
// means the repository is not signed.
type Resolver interface {
	SignerForRepo(ctx context.Context, repoID string) (gpg.Signer, error)
}
