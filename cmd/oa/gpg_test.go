// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/http"
	"testing"
)

func TestContractGPGDelete(t *testing.T) {
	const (
		id    = "0f0e0d0c-0b0a-4908-8706-050403020100"
		inUse = `{"error":"the key is active; deactivate it first or pass force=true","code":"key_in_use"}`
		path  = "DELETE /api/v1/admin/gpg/keys/{id}"
		list  = `{"can_store_keys":true,"keys":[{"id":"0f0e0d0c-0b0a-4908-8706-050403020100","name":"release","fingerprint":"AAAA"}]}`
	)

	runCases(t, []contractCase{
		{
			name:    "an active key points at --force, not the API parameter",
			args:    []string{"gpg", "delete", id},
			replies: map[string]reply{"GET /api/v1/admin/gpg/keys": ok(list), path: {status: http.StatusConflict, body: inUse}},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/gpg/keys"}, {method: "DELETE", pattern: "/api/v1/admin/gpg/keys/{id}"}},
			fail:    "--force",
		},
		{
			name:    "--force deletes it",
			args:    []string{"gpg", "delete", id, "--force"},
			replies: map[string]reply{"GET /api/v1/admin/gpg/keys": ok(list), path: {status: http.StatusNoContent}},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/gpg/keys"}, {method: "DELETE", pattern: "/api/v1/admin/gpg/keys/{id}", query: map[string]string{"force": "true"}}},
			out:     "GPG key deleted.",
		},
		{
			name:    "other conflicts keep the server message",
			args:    []string{"gpg", "delete", id},
			replies: map[string]reply{"GET /api/v1/admin/gpg/keys": ok(list), path: {status: http.StatusConflict, body: `{"error":"the config key is read-only","code":"read_only"}`}},
			calls:   []call{{method: "GET", pattern: "/api/v1/admin/gpg/keys"}, {method: "DELETE", pattern: "/api/v1/admin/gpg/keys/{id}"}},
			fail:    "read-only",
		},
	})
}
