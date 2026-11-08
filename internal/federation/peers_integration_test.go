// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package federation

import (
	"context"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/crypto"
)

// A patch made while the encryption key cannot open the stored secrets must
// not overwrite the ciphertext of the secrets it does not carry.
func TestUpdatePeerKeepsUnreadableSecretsUntouched(t *testing.T) {
	ctx := context.Background()
	db := freshDB(t).GetDB()
	store := NewStore(db)
	repoID := seedRepo(t, db, "npm-peer-secrets", "npm")
	enableLog(t, db, repoID)

	keyA, keyB := strings.Repeat("a", crypto.MinKeyLen), strings.Repeat("b", crypto.MinKeyLen)

	t.Cleanup(func() { _ = crypto.Init("") })

	if err := crypto.Init(keyA); err != nil {
		t.Fatalf("init key: %v", err)
	}

	peer, err := store.CreatePeer(ctx, NewPeer{
		RepoID: repoID, Name: "p", BaseURL: "https://peer.example.com",
		Token: "tok", ClientCert: "cert", ClientKey: "key",
	})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}

	if err := crypto.Init(keyB); err != nil {
		t.Fatalf("init wrong key: %v", err)
	}

	name := "renamed"

	if _, err := store.UpdatePeer(ctx, repoID, peer.ID, PeerPatch{Name: &name}); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if err := crypto.Init(keyA); err != nil {
		t.Fatalf("restore key: %v", err)
	}

	got, err := store.GetPeer(ctx, repoID, peer.ID)
	if err != nil {
		t.Fatalf("get peer: %v", err)
	}

	if got.Name != name {
		t.Errorf("name = %q, want %q", got.Name, name)
	}

	if got.TokenEnc != peer.TokenEnc || got.ClientCertEnc != peer.ClientCertEnc || got.ClientKeyEnc != peer.ClientKeyEnc {
		t.Error("secret ciphertext changed by a patch that did not carry any secret")
	}

	secrets, err := got.Secrets()
	if err != nil {
		t.Fatalf("secrets after restoring the key: %v", err)
	}

	if secrets.Token != "tok" || secrets.ClientCert != "cert" || secrets.ClientKey != "key" {
		t.Errorf("secrets = %+v, want the originals", secrets)
	}
}
