// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"fmt"

	"github.com/M0Rf30/pkgformat/gpg"
	formatdeb "github.com/openartifact/openartifact/internal/format/deb"
	formatrpm "github.com/openartifact/openartifact/internal/format/rpm"
)

// addSignedRelease adds InRelease and Release.gpg for release under prefix. A
// signing failure is returned: publishing the unsigned Release would replace
// the previous signed set, which every verifying client then rejects, whereas
// failing keeps the old set and lets the regeneration retry.
func addSignedRelease(blobs map[string][]byte, prefix string, release []byte, signer gpg.Signer) error {
	signed, err := formatdeb.NewReleaseSigner(signer).SignRelease(release)
	if err != nil {
		return fmt.Errorf("signing Release: %w", err)
	}

	blobs[prefix+"InRelease"] = signed.InRelease
	blobs[prefix+"Release.gpg"] = signed.ReleaseGPG

	return nil
}

// addSignedRepomd adds repodata/repomd.xml.asc for repomd; a signing failure is
// returned for the reason given at addSignedRelease.
func addSignedRepomd(blobs map[string][]byte, repomd []byte, signer gpg.Signer) error {
	signature, err := formatrpm.SignRepomdXML(signer, repomd)
	if err != nil {
		return fmt.Errorf("signing repomd.xml: %w", err)
	}

	blobs["repodata/repomd.xml.asc"] = signature

	return nil
}
