// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"context"
	"encoding/hex"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/pkg/nix"
)

// The pieces below are what a group repository (internal/group) shares with
// the hosted handler: the documents a binary cache answers itself and the
// signing of a narinfo.

// ServeCacheInfo answers /nix-cache-info of a repository of ours.
func ServeCacheInfo(w http.ResponseWriter, r *http.Request) {
	serveText(w, r, "StoreDir: "+nix.StoreDir+"\nWantMassQuery: 1\nPriority: "+strconv.Itoa(cachePriority)+"\n")
}

// SigningKey returns the key that signs the narinfo of repo, nil when there is
// none to sign with (no provider, or no OPENARTIFACT_SECRET_KEY).
func SigningKey(ctx context.Context, keys KeyProvider, repo *models.Repository) (*nix.SecretKey, error) {
	if keys == nil {
		return nil, nil //nolint:nilnil // no provider: unsigned
	}

	return keys.SecretKeyForRepo(ctx, repo)
}

// ServePublicKey answers the "name:base64" line a client puts into
// trusted-public-keys: the public half of repo's own key. A group repository is
// served by the group handler through it with the group's key.
func ServePublicKey(w http.ResponseWriter, r *http.Request, log *logger.Logger, keys KeyProvider, repo *models.Repository) {
	key, err := SigningKey(r.Context(), keys, repo)
	if err != nil {
		log.Errorw("failed to load nix signing key", "repo", repo.Name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	if key == nil {
		http.Error(w, "this repository has no signing key (OPENARTIFACT_SECRET_KEY is not set)", http.StatusNotFound)
		return
	}

	serveText(w, r, key.PublicKey()+"\n")
}

// SignNarInfo adds key's signature to n. A signature already on n by a key of
// the same name is dropped: the repository signs with its current key, so it
// is from an earlier generation of it. Signatures by other keys stay.
func SignNarInfo(n *nix.NarInfo, key *nix.SecretKey) error {
	sig, err := key.Sign(n)
	if err != nil {
		return err //nolint:wrapcheck // the caller names the path
	}

	kept := n.Sigs[:0:0]

	for _, s := range n.Sigs {
		if !strings.HasPrefix(s, key.Name()+":") {
			kept = append(kept, s)
		}
	}

	n.Sigs = append(kept, sig)

	return nil
}

// WriteNarInfo answers a GET or HEAD with the text of a narinfo, honoring
// If-None-Match. The validator is not a security digest: the body changes with
// the signing key.
func WriteNarInfo(w http.ResponseWriter, r *http.Request, public bool, body string) {
	tag := fnv.New128a()
	_, _ = tag.Write([]byte(body))

	if base.Revalidate(w, r, public, `"`+hex.EncodeToString(tag.Sum(nil))+`"`, narinfoMaxAge) {
		return
	}

	w.Header().Set("Content-Type", contentTypeNarInfo)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}
