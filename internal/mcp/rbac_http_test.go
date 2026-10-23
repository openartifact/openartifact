// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

// toolText returns the first text block of a tool result.
func toolText(t *testing.T, resp toolResponse) string {
	t.Helper()

	if resp.Error != nil {
		t.Fatalf("protocol error: %+v", resp.Error)
	}

	if len(resp.Result.Content) == 0 {
		t.Fatal("tool result without content")
	}

	return resp.Result.Content[0].Text
}

func repoNames(t *testing.T, resp toolResponse) []string {
	t.Helper()

	if resp.Result.IsError {
		t.Fatalf("tool error: %s", toolText(t, resp))
	}

	items, _ := resp.Result.StructuredContent["repositories"].([]any)

	var names []string

	for _, it := range items {
		names = append(names, it.(map[string]any)["name"].(string))
	}

	slices.Sort(names)

	return names
}

func TestReadOnlyTokenUsesReadTools(t *testing.T) {
	st := newStack(t, nil)

	tests := []struct {
		tool string
		args map[string]any
	}{
		{"list_repositories", map[string]any{}},
		{"get_repository", map[string]any{"name": "web-npm"}},
		{"list_packages", map[string]any{"repository": "web-npm"}},
		{"get_package", map[string]any{"repository": "web-npm", "name": "left-pad"}},
		{"client_setup", map[string]any{"repository": "web-npm"}},
		{"search_artifacts", map[string]any{"query": "left"}},
		{"list_ai_analyses", map[string]any{"repository_id": "r-npm"}},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			resp := st.call("reader", tt.tool, tt.args)
			if resp.Error != nil || resp.Result.IsError {
				t.Fatalf("a read-only token must be able to use %s: %+v %s", tt.tool, resp.Error, resp.Result.Content)
			}
		})
	}
}

func TestReadOnlyTokenSeesOnlyItsRepositories(t *testing.T) {
	st := newStack(t, nil)

	got := repoNames(t, st.call("reader", "list_repositories", map[string]any{}))
	if want := []string{"images", "web-npm"}; !slices.Equal(got, want) {
		t.Fatalf("reader sees %v, want %v", got, want)
	}

	all := repoNames(t, st.call("admin", "list_repositories", map[string]any{}))
	if len(all) != 3 {
		t.Fatalf("admin sees %v, want all three repositories", all)
	}

	hidden := st.call("reader", "get_repository", map[string]any{"name": "hidden"})
	if !hidden.Result.IsError || toolText(t, hidden) != msgNotFound {
		t.Fatalf("hidden repository must look absent, got %+v", hidden.Result)
	}

	missing := st.call("reader", "get_repository", map[string]any{"name": "does-not-exist"})
	if toolText(t, missing) != toolText(t, hidden) {
		t.Fatal("a denied and a missing repository must be indistinguishable")
	}
}

func TestReadOnlyTokenCannotWriteOrManage(t *testing.T) {
	st := newStack(t, func(o *stackOptions) { o.aiEnabled = true })

	trigger := st.call("reader", "trigger_ai_analysis", map[string]any{"repository_id": "r-npm", "artifact_id": "c1"})
	if !trigger.Result.IsError || toolText(t, trigger) != msgNotFound {
		t.Fatalf("trigger_ai_analysis with a read-only token: %+v", trigger.Result)
	}

	vulns := st.call("reader", "get_vulnerabilities", map[string]any{"repository": "web-npm"})
	if !vulns.Result.IsError || toolText(t, vulns) != msgAdminRequired {
		t.Fatalf("get_vulnerabilities with a read-only token: %+v", vulns.Result)
	}
}

func TestAdminTokenUsesWriteAndManageTools(t *testing.T) {
	st := newStack(t, func(o *stackOptions) { o.aiEnabled = true })

	vulns := st.call("admin", "get_vulnerabilities", map[string]any{"repository": "web-npm"})
	if vulns.Result.IsError {
		t.Fatalf("get_vulnerabilities: %s", toolText(t, vulns))
	}

	trigger := st.call("admin", "trigger_ai_analysis", map[string]any{"repository_id": "r-npm", "artifact_id": "c1"})
	if trigger.Result.IsError {
		t.Fatalf("trigger_ai_analysis: %s", toolText(t, trigger))
	}
}

func TestTokenScopeNarrowsAdminToReadOnly(t *testing.T) {
	st := newStack(t, nil)
	st.addUser("scoped", targetAdmin, []string{targetReadOnly})
	st.db.targets["scoped"] = append(st.db.targets["scoped"], st.db.targets["reader"]...)

	got := repoNames(t, st.call("scoped", "list_repositories", map[string]any{}))
	if want := []string{"images", "web-npm"}; !slices.Equal(got, want) {
		t.Fatalf("a token scoped to the read-only target sees %v, want %v", got, want)
	}
}

func TestPerToolErrorsAreNotHTTPErrors(t *testing.T) {
	st := newStack(t, nil)

	rec := st.do(http.MethodPost, "reader", rpcBody(t, 1, "tools/call", map[string]any{
		"name": "get_vulnerabilities", "arguments": map[string]any{},
	}), nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("a per-tool refusal is a tool error inside a 200 response, got %d", rec.Code)
	}
}

const errArtifactMissing = "artifact not found in this repository"

func TestTriggerAIAnalysisAlwaysChecksTheArtifact(t *testing.T) {
	st := newStack(t, func(o *stackOptions) { o.aiEnabled = true })

	tests := []struct {
		name    string
		user    string
		args    map[string]any
		wantErr string // "" = accepted
	}{
		{"existing artifact", "admin", map[string]any{"repository_id": "r-npm", "artifact_id": "c1"}, ""},
		{"unknown artifact", "admin", map[string]any{"repository_id": "r-npm", "artifact_id": "nope"}, errArtifactMissing},
		{"unknown artifact with caller manifest text", "admin", map[string]any{"repository_id": "r-npm", "artifact_id": "nope", "manifest_text": "{}"}, errArtifactMissing},
		{"artifact of another repository", "admin", map[string]any{"repository_id": "r-docker", "artifact_id": "c1", "manifest_text": "{}"}, errArtifactMissing},
		{"unknown repository", "admin", map[string]any{"repository_id": "r-none", "artifact_id": "c1", "manifest_text": "{}"}, msgNotFound},
		{"existing artifact with manifest text", "admin", map[string]any{"repository_id": "r-npm", "artifact_id": "c1", "manifest_text": "{}"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := st.call(tt.user, "trigger_ai_analysis", tt.args)
			if tt.wantErr == "" {
				if resp.Result.IsError {
					t.Fatalf("want accepted, got %s", toolText(t, resp))
				}

				return
			}

			if !resp.Result.IsError || !strings.Contains(toolText(t, resp), tt.wantErr) {
				t.Fatalf("want error %q, got %+v", tt.wantErr, resp.Result)
			}
		})
	}
}

func TestTriggerAIAnalysisIsRateLimitedPerPrincipal(t *testing.T) {
	st := newStack(t, func(o *stackOptions) {
		o.aiEnabled = true
		o.server.AITriggerPerMinute = 2
	})

	args := map[string]any{"repository_id": "r-npm", "artifact_id": "c1"}

	for i := range 2 {
		if resp := st.call("admin", "trigger_ai_analysis", args); resp.Result.IsError {
			t.Fatalf("call %d: %s", i+1, toolText(t, resp))
		}
	}

	limited := st.call("admin", "trigger_ai_analysis", args)
	if !limited.Result.IsError || !strings.Contains(toolText(t, limited), "rate limit") {
		t.Fatalf("third trigger: %+v", limited.Result)
	}
}

func TestServiceAccountTokensAreRefused(t *testing.T) {
	st := newStack(t, nil)
	st.addUser("svc", targetAdmin, nil)

	project := "proj-1"
	st.db.users["svc"].ProjectID = &project
	st.db.users["svc"].Kind = models.UserKindService

	for _, tok := range st.db.tokens {
		if tok.UserID == "svc" {
			tok.Kind = auth.TokenKindDeploy
			tok.ProjectID = &project
		}
	}

	if rec := st.do(http.MethodPost, "svc", rpcBody(t, 1, "tools/list", nil), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("a project service account is a repository credential only, got %d", rec.Code)
	}
}
