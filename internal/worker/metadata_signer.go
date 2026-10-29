// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"fmt"

	"github.com/openartifact/openartifact/pkg/gpg"
)

// metadataSigner returns the signer of the repository's metadata, nil when it
// is unsigned. A resolution failure aborts the regeneration: replacing signed
// metadata with unsigned metadata because of a transient error would break
// every client that verifies it. who is the log prefix of the calling worker,
// e.g. "deb worker".
func metadataSigner(ctx context.Context, signers gpg.Resolver, repoID, who string) (gpg.Signer, error) {
	if signers == nil {
		return nil, nil //nolint:nilnil // nil signer means unsigned
	}

	signer, err := signers.SignerForRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("%s: resolving signing key: %w", who, err)
	}

	return signer, nil
}
