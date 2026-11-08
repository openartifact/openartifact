// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The MCP tools mirror the read-only REST API. This file is the guard that
// keeps them in step: every GET capability under /api/v1 in the OpenAPI spec
// must have a decision in driftTable, either the MCP tools that serve it or the
// reason the assistant surface leaves it out. A new GET endpoint with no
// decision fails TestEveryGETCapabilityHasADecision; the fix is a row below.
//
// A capability is the static path of an operation: the segments after
// /api/v1 without the {parameters}, so GET /repositories/{name}/vuln/scans and
// GET /repositories/{name}/vuln/scans/{id} are both "repositories/vuln/scans".
// A pattern "prefix/**" decides the prefix and every capability under it at once;
// an exact pattern wins over a prefix, a longer prefix over a shorter one.
type driftDecision struct {
	pattern string
	// tools are the MCP tools that serve the capability; empty for an exclusion.
	tools []string
	// reason says why the capability is not an MCP tool; required without tools.
	reason string
}

const (
	reasonControlPlane = "control plane: MCP is the read-only assistant surface for repository content, administration stays in the API and the CLI"
	reasonIdentity     = "identity, credentials and access management: nothing an assistant should read or relay"
	reasonDashboard    = "cross-repository dashboard feed; the per-repository tools answer the same questions within the caller's access"
	reasonBinary       = "binary or export download; MCP returns structured text, not files"
	reasonRepoConfig   = "repository configuration and operations owned by repository admins; not read by assistants"
)

//nolint:gochecknoglobals // immutable decision table
var driftTable = []driftDecision{
	// Repositories and packages.
	{pattern: "repositories", tools: []string{"list_repositories"}},
	{pattern: "repository", tools: []string{"get_repository"}},
	{pattern: "repositories/packages", tools: []string{"list_packages", "get_package"}},
	{pattern: "search", tools: []string{"search_artifacts"}},
	{pattern: "repositories/ai-analysis", tools: []string{"list_ai_analyses", "get_artifact_analysis"}},
	{pattern: "repositories/vuln/scans", tools: []string{"get_vulnerabilities"}},

	// Availability and the minimum-age gate.
	{pattern: "repositories/availability", tools: []string{"check_availability"}},
	{pattern: "repositories/held-back-versions", tools: []string{"list_held_back_versions"}},
	{pattern: "repositories/proxy-versions", tools: []string{"list_proxy_versions"}},

	// Compliance reports and the activity feed.
	{pattern: "repositories/license/scans", tools: []string{"get_licenses"}},
	{pattern: "repositories/sbom", tools: []string{"get_sbom"}},
	{pattern: "repositories/activity", tools: []string{"get_repository_activity"}},

	// Repository-level capabilities left out.
	{pattern: "repositories/packages/download", reason: reasonBinary},
	{pattern: "repositories/packages/delta", reason: reasonBinary},
	{pattern: "repositories/packages/deltas", reason: reasonBinary},
	{pattern: "repositories/sbom/download", reason: "the raw SBOM document; get_sbom returns its summary and component list"},
	{pattern: "repositories/vuln/scans/export", reason: reasonBinary},
	{
		pattern: "repositories/vuln/scans/gate",
		reason:  "CI gate verdict for a scan; get_vulnerabilities reports the findings it is computed from",
	},
	{
		pattern: "repositories/gate",
		reason:  "CI gate verdict for an image digest; get_vulnerabilities reports the findings it is computed from",
	},
	{pattern: "repositories/attestations", reason: "signature and provenance records of images; not part of the assistant surface yet"},
	{pattern: "repositories/attestation-trust", reason: reasonRepoConfig},
	{pattern: "repositories/vuln/scanners", reason: reasonRepoConfig},
	{pattern: "repositories/secrets/**", reason: "secret-scan findings name leaked credentials; never relayed to an assistant"},
	{pattern: "repositories/license/policies", reason: reasonRepoConfig},
	{pattern: "repositories/artifacts/properties", reason: "artifact property reads; not part of the assistant surface yet"},
	{pattern: "repositories/artifacts/search", reason: "property search (key/value); search_artifacts searches by name"},
	{pattern: "repositories/apk-key", reason: "public signing key download; client_setup tells where the clients fetch it"},
	{pattern: "repositories/nix-key", reason: "public signing key download; client_setup tells where the clients fetch it"},
	{pattern: "repositories/attestation-attach", reason: reasonRepoConfig},
	{pattern: "repositories/cleanup-policies", reason: reasonRepoConfig},
	{
		pattern: "repositories/curation-rules",
		reason:  reasonRepoConfig + "; check_availability and list_held_back_versions report their effect",
	},
	{pattern: "repositories/dependency-track/**", reason: reasonRepoConfig},
	{pattern: "repositories/federation/**", reason: reasonRepoConfig},
	{pattern: "repositories/members", reason: reasonIdentity},
	{pattern: "repositories/quota", reason: reasonRepoConfig},
	{pattern: "repositories/usage", reason: reasonRepoConfig},
	{pattern: "repositories/trash", reason: reasonRepoConfig},
	{pattern: "repositories/webhooks", reason: reasonRepoConfig},
	{pattern: "repositories/security/summary", reason: reasonDashboard},
	{pattern: "repositories/vex/**", reason: reasonRepoConfig},

	// Cross-repository dashboards.
	{pattern: "packages/recent", reason: reasonDashboard},
	{pattern: "security/**", reason: reasonDashboard},
	{pattern: "vuln/**", reason: reasonDashboard},
	{pattern: "trash", reason: reasonDashboard},

	// Control plane, identity and platform.
	{pattern: "admin/**", reason: reasonControlPlane},
	{pattern: "auth/**", reason: reasonIdentity},
	{pattern: "tokens", reason: reasonIdentity},
	{pattern: "users/**", reason: reasonIdentity},
	{pattern: "groups/**", reason: reasonIdentity},
	{pattern: "service-accounts/**", reason: reasonIdentity},
	{pattern: "permission-targets/**", reason: reasonIdentity},
	{pattern: "oidc-trust-policies/**", reason: reasonIdentity},
	{pattern: "projects/**", reason: reasonControlPlane},
	{pattern: "bundles/**", reason: reasonControlPlane},
	{pattern: "pipelines/**", reason: reasonControlPlane},
	{pattern: "webhooks/**", reason: reasonControlPlane},
	{pattern: "branding/**", reason: "UI branding assets"},
	{pattern: "telemetry/**", reason: "frontend telemetry sink"},
	{pattern: "system/info", reason: "instance version and build information; not repository content"},
	{pattern: "openapi.json", reason: "the API description itself"},
	{pattern: "openapi.yaml", reason: "the API description itself"},
}

// mcpOnlyTools have no GET counterpart in the REST API: they are written for
// the assistant (client_setup renders instructions, trigger_ai_analysis is a
// POST that is rate limited and not read-only).
//
//nolint:gochecknoglobals // immutable lookup table
var mcpOnlyTools = map[string]string{
	"client_setup":        "renders package-manager configuration for a repository; no REST counterpart",
	"trigger_ai_analysis": "starts an analysis (a POST in the REST API); the only tool that writes",
}

// getCapabilities reads the GET operations under /api/v1 of the OpenAPI spec and
// groups them by static path; the value is one example operation per group.
func getCapabilities(t *testing.T) map[string]string {
	t.Helper()

	raw, err := os.ReadFile("../api/openapi.json")
	if err != nil {
		t.Fatalf("read the OpenAPI spec: %v", err)
	}

	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}

	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("decode the OpenAPI spec: %v", err)
	}

	const prefix = "/api/v1/"

	groups := map[string]string{}

	for path, ops := range spec.Paths {
		if _, ok := ops["get"]; !ok || !strings.HasPrefix(path, prefix) {
			continue
		}

		var static []string

		for seg := range strings.SplitSeq(strings.TrimPrefix(path, prefix), "/") {
			if !strings.HasPrefix(seg, "{") {
				static = append(static, seg)
			}
		}

		key := strings.Join(static, "/")
		if cur, ok := groups[key]; !ok || path < cur {
			groups[key] = path
		}
	}

	return groups
}

// decide returns the table row that decides a capability: the exact pattern, or
// else the longest "prefix/**" pattern above it.
func decide(capability string) (driftDecision, bool) {
	var (
		best    driftDecision
		bestLen = -1
	)

	for _, d := range driftTable {
		switch {
		case d.pattern == capability:
			return d, true
		case strings.HasSuffix(d.pattern, "/**"):
			base := strings.TrimSuffix(d.pattern, "/**")
			if (capability == base || strings.HasPrefix(capability, base+"/")) && len(base) > bestLen {
				best, bestLen = d, len(base)
			}
		}
	}

	return best, bestLen >= 0
}

func TestEveryGETCapabilityHasADecision(t *testing.T) {
	var undecided []string

	for capability, example := range getCapabilities(t) {
		if _, ok := decide(capability); !ok {
			undecided = append(undecided, "GET "+example+" (capability "+capability+")")
		}
	}

	sort.Strings(undecided)

	if len(undecided) > 0 {
		t.Fatalf("GET capabilities of the REST API without a decision for the MCP server. Map each to a tool "+
			"(or exclude it with a reason) in driftTable of drift_test.go:\n  %s", strings.Join(undecided, "\n  "))
	}
}

func TestDriftTableHasNoStaleOrMalformedRows(t *testing.T) {
	capabilities := getCapabilities(t)
	known := allTools()
	seen := map[string]bool{}

	for _, d := range driftTable {
		if seen[d.pattern] {
			t.Errorf("duplicate row %q", d.pattern)
		}

		seen[d.pattern] = true

		if len(d.tools) == 0 && d.reason == "" {
			t.Errorf("row %q is neither mapped to a tool nor excluded with a reason", d.pattern)
		}

		for _, tool := range d.tools {
			if !slices.Contains(known, tool) {
				t.Errorf("row %q maps to %q, which is not a registered tool", d.pattern, tool)
			}
		}

		matched := false

		for capability := range capabilities {
			if got, ok := decide(capability); ok && got.pattern == d.pattern {
				matched = true
				break
			}
		}

		if !matched {
			t.Errorf("row %q decides no capability of the OpenAPI spec: remove it or fix the pattern", d.pattern)
		}
	}
}

func TestEveryToolIsMappedOrMCPOnly(t *testing.T) {
	mapped := map[string]bool{}

	for _, d := range driftTable {
		for _, tool := range d.tools {
			mapped[tool] = true
		}
	}

	for _, tool := range allTools() {
		_, mcpOnly := mcpOnlyTools[tool]

		if mapped[tool] == mcpOnly {
			t.Errorf(
				"tool %q must appear in exactly one of driftTable (REST counterpart) and mcpOnlyTools (no counterpart); mapped=%v",
				tool,
				mapped[tool],
			)
		}
	}

	for tool := range mcpOnlyTools {
		if !slices.Contains(allTools(), tool) {
			t.Errorf("mcpOnlyTools names %q, which is not a registered tool", tool)
		}
	}
}

// TestDriftGuardFailsOnANewCapability proves the guard has teeth: a GET
// capability absent from the table is reported as undecided.
func TestDriftGuardFailsOnANewCapability(t *testing.T) {
	if _, ok := decide("repositories/brand-new-feature"); ok {
		t.Fatal("a capability that is not in the table must be undecided")
	}

	if _, ok := decide("admin/anything/at/all"); !ok {
		t.Fatal("a prefix row decides everything below it")
	}

	if d, _ := decide("repositories/vuln/scans/gate"); !strings.Contains(d.reason, "CI gate") {
		t.Fatalf("an exact row wins over a shorter one: %+v", d)
	}
}
