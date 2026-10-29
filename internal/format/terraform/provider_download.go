// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/urlparams"
	"github.com/openartifact/openartifact/pkg/gpg"
)

// errNoSigner means the repository has no GPG signing key, so a provider
// cannot be offered: Terraform refuses a provider whose checksum file is not
// signed.
var errNoSigner = errors.New("no GPG signing key is configured for this repository")

// signerFor returns the signer of the repository, errNoSigner when it has none.
func (h *HTTPHandler) signerFor(ctx context.Context, repoID string) (gpg.Signer, error) {
	if h.signers == nil {
		return nil, errNoSigner
	}

	signer, err := h.signers.SignerForRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("resolve signer: %w", err)
	}

	if signer == nil {
		return nil, errNoSigner
	}

	return signer, nil
}

// providerPlatform reads and validates {type}/{version}/{os}/{arch}; it
// writes the 404 itself for coordinates no upload could have used.
func providerPlatform(w http.ResponseWriter, r *http.Request) (string, string, string, string, bool) {
	vars := urlparams.Vars(r)
	typ, version, osName, arch := vars["type"], vars["version"], vars["os"], vars["arch"]

	if !validName(typ) || !validVersion(version) || !validPlatformPart(osName) || !validPlatformPart(arch) {
		writeErrors(w, http.StatusNotFound, "provider package not found")
		return "", "", "", "", false
	}

	return typ, version, osName, arch, true
}

// signingKeyJSON is the public key a client verifies the checksum file with.
type signingKeyJSON struct {
	KeyID      string `json:"key_id"`
	ASCIIArmor string `json:"ascii_armor"`
}

// handleProviderDownload answers GET /{type}/{version}/download/{os}/{arch}
// with the package description: ticketed URLs for the zip, the checksum file
// and its signature, and the key the signature verifies with.
func (h *HTTPHandler) handleProviderDownload(w http.ResponseWriter, r *http.Request) {
	repo := h.resolveRepo(w, r)
	if repo == nil {
		return
	}

	typ, version, osName, arch, ok := providerPlatform(w, r)
	if !ok {
		return
	}

	p, err := h.DB.GetTerraformProvider(r.Context(), repo.ID, typ, version, osName, arch)
	if err != nil {
		h.Log.Errorw("failed to get terraform provider", "repo", repo.Name, "type", typ, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	if p == nil {
		writeErrors(w, http.StatusNotFound, "provider package not found")
		return
	}

	zipName := providerZipName(typ, version, osName, arch)

	keys := quarantine.PathKeys(p.ID, "providers/"+typ+"/"+version+"/"+osName+"/"+arch, zipName, r.URL.Path)
	if h.DenyIfQuarantined(w, r, repo.ID, keys...) {
		return
	}

	signer, err := h.signerFor(r.Context(), repo.ID)
	if err != nil {
		if !errors.Is(err, errNoSigner) {
			h.Log.Errorw("failed to resolve repository signer", "repo", repo.Name, "error", err)
		}

		writeErrors(w, http.StatusServiceUnavailable, errNoSigner.Error())

		return
	}

	armored, err := signer.PublicKey()
	if err != nil {
		h.Log.Errorw("failed to read the repository public key", "repo", repo.Name, "error", err)
		writeErrors(w, http.StatusInternalServerError, "internal error")

		return
	}

	urls, ok := h.providerURLs(w, repo.ID, p)
	if !ok {
		return
	}

	if err := h.DB.IncrementTerraformProviderDownloadCount(r.Context(), repo.ID, p.ID); err != nil {
		h.Log.Warnw("failed to count terraform provider download", "id", p.ID, "error", err)
	}

	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"protocols":             p.Protocols,
		"os":                    osName,
		"arch":                  arch,
		"filename":              zipName,
		"download_url":          urls.zip,
		"shasums_url":           urls.sums,
		"shasums_signature_url": urls.sig,
		"shasum":                p.Checksum,
		"signing_keys": map[string][]signingKeyJSON{
			"gpg_public_keys": {{KeyID: keyID(signer.KeyFingerprint()), ASCIIArmor: string(armored)}},
		},
	})
}

type providerLinks struct{ zip, sums, sig string }

// providerURLs mints the three ticketed URLs of a provider package.
func (h *HTTPHandler) providerURLs(w http.ResponseWriter, repoID string, p *models.TerraformProvider) (providerLinks, bool) {
	var (
		links providerLinks
		ok    bool
	)

	if links.zip, ok = h.mintURL(w, ticket{
		repoID: repoID, kind: kindProviderZip, a: p.Type, b: p.Version, c: platformName(p.OS, p.Arch),
	}, providerZipName(p.Type, p.Version, p.OS, p.Arch)); !ok {
		return providerLinks{}, false
	}

	sumsName := providerSumsName(p.Type, p.Version)
	sums := ticket{repoID: repoID, kind: kindProviderSums, a: p.Type, b: p.Version}

	// The platform rides along so that redeeming the checksum file re-checks
	// the quarantine of the package the client is fetching.
	sums.c = platformName(p.OS, p.Arch)

	if links.sums, ok = h.mintURL(w, sums, sumsName); !ok {
		return providerLinks{}, false
	}

	sums.kind = kindProviderSumSig

	if links.sig, ok = h.mintURL(w, sums, sumsName+".sig"); !ok {
		return providerLinks{}, false
	}

	return links, true
}
