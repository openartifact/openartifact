// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/audit"
)

// Every federation change (enable, peers, resync, tokens) leaves an audit event
// for its repository, and none of them carries a peer or token secret.
func TestFederationChangesAreAudited(t *testing.T) {
	rows := recordAudit(t)

	e := newFedAPIEnv(t, true)
	repoID := fedAPISeedRepo(t, e.db, "files", "raw", "hosted")

	e.enable(t, "files")
	peer := e.addPeer(t, "files", "east")

	steps := []struct{ method, path, body string }{
		{http.MethodPatch, fedAPIPath("files", "/peers/"+peer), `{"name":"east2","token":"another-secret"}`},
		{http.MethodPost, fedAPIPath("files", "/peers/"+peer+"/resync"), ""},
	}

	for _, s := range steps {
		if code, _, raw := e.do(t, fedAPIAdmin, s.method, s.path, s.body); code >= http.StatusBadRequest {
			t.Fatalf("%s %s: %d %s", s.method, s.path, code, raw)
		}
	}

	code, m, raw := e.do(t, fedAPIAdmin, http.MethodPost, fedAPIPath("files", "/tokens"), `{"name":"east-pull"}`)
	if code != http.StatusCreated {
		t.Fatalf("create token: %d %s", code, raw)
	}

	token, _ := m["token"].(string)
	tokenID, _ := m["id"].(string)

	for _, path := range []string{"/tokens/" + tokenID, "/peers/" + peer} {
		if code, _, raw := e.do(t, fedAPIAdmin, http.MethodDelete, fedAPIPath("files", path), ""); code != http.StatusNoContent {
			t.Fatalf("DELETE %s: %d %s", path, code, raw)
		}
	}

	got := wantAudit(t, rows(),
		audit.ActionFederationEnabledChanged,
		audit.ActionFederationPeerCreated,
		audit.ActionFederationPeerUpdated,
		audit.ActionFederationPeerResynced,
		audit.ActionFederationTokenCreated,
		audit.ActionFederationTokenRevoked,
		audit.ActionFederationPeerDeleted,
	)

	wantMessage(t, got[0], "files", "enabled")
	wantMessage(t, got[1], "east")
	wantMessage(t, got[2], "east", "name", "token")
	wantMessage(t, got[4], "east-pull")

	for _, row := range got {
		if row.RepositoryID == nil || *row.RepositoryID != repoID {
			t.Errorf("%s repository id = %v, want %s", row.Action, row.RepositoryID, repoID)
		}

		d := string(row.DetailsJSON)
		if strings.Contains(d, "another-secret") || strings.Contains(d, "peer-secret-token") ||
			(token != "" && strings.Contains(d, token)) {
			t.Errorf("%s details leak a secret: %s", row.Action, d)
		}
	}
}
