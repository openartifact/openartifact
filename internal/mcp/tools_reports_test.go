// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/database/models"
)

func TestGetLicenses(t *testing.T) {
	st := newStack(t, nil)

	scanned := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	st.db.gate.license = &models.LicenseScanResult{
		ID: "l1", RepositoryID: "r-npm", ArtifactID: "c1",
		Licenses:           []string{"MIT", injection + strings.Repeat("x", 3*maxLicenseText)},
		SPDXExpressions:    []string{"MIT"},
		UnresolvedLicenses: []string{"Some Custom License"},
		LicenseSource:      "artifact", ScanStatus: "completed", ScanError: "open /srv/oa/secret/path: permission denied",
		Quarantined: true, ScannedAt: &scanned,
	}

	args := map[string]any{"repository": "web-npm", "artifact_id": "c1"}

	resp := st.call("admin", "get_licenses", args)
	if resp.Result.IsError {
		t.Fatal(toolText(t, resp))
	}

	sc := resp.Result.StructuredContent
	if sc["content_trust"] != "untrusted" || sc["repository"] != "web-npm" || sc["quarantined"] != true ||
		sc["scan_status"] != "completed" {
		t.Fatalf("result = %v", sc)
	}

	licenses := sc["licenses"].([]any)
	if len(licenses) != 2 || len(licenses[1].(string)) > maxLicenseText {
		t.Errorf("license text is repository-provided and must be capped at %d bytes: %v", maxLicenseText, licenses)
	}

	if strings.Contains(toolText(t, resp), "/srv/oa/secret") {
		t.Error("the scan error text must not be relayed")
	}

	if reader := st.call("reader", "get_licenses", args); toolText(t, reader) != msgAdminRequired {
		t.Errorf("reader: %q, want %q", toolText(t, reader), msgAdminRequired)
	}

	if unknown := st.call(
		"admin",
		"get_licenses",
		map[string]any{"repository": "nope", "artifact_id": "c1"},
	); toolText(
		t,
		unknown,
	) != msgNotFound {
		t.Errorf("unknown repository: %q", toolText(t, unknown))
	}

	if noID := st.call(
		"admin",
		"get_licenses",
		map[string]any{"repository": "web-npm", "artifact_id": ""},
	); toolText(
		t,
		noID,
	) != errArtifactIDParam.Error() {
		t.Errorf("missing artifact_id: %q", toolText(t, noID))
	}

	st.db.gate.license = nil

	if none := st.call("admin", "get_licenses", args); !none.Result.IsError || toolText(t, none) != errNoLicenseScan.Error() {
		t.Errorf("no scan: %q", toolText(t, none))
	}
}

// cdxDoc is a CycloneDX document naming n components.
func cdxDoc(n int) string {
	var b strings.Builder

	b.WriteString(`{"bomFormat":"CycloneDX","specVersion":"1.5","components":[`)

	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}

		fmt.Fprintf(
			&b,
			`{"name":"dep-%d","version":"1.0.%d","purl":"pkg:npm/dep-%d@1.0.%d","licenses":[{"license":{"id":"MIT"}}]}`,
			i,
			i,
			i,
			i,
		)
	}

	b.WriteString(`]}`)

	return b.String()
}

const spdxDoc = `{"spdxVersion":"SPDX-2.3","packages":[{"name":"` + injection + `","versionInfo":"2.0","licenseConcluded":"Apache-2.0"}]}`

func sbomFixture(st *stack, components int) {
	newer := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	older := newer.Add(-24 * time.Hour)

	st.db.gate.blobs = fakeBlobs{"sha-cdx": cdxDoc(components), "sha-spdx": spdxDoc}
	st.db.gate.sboms = []*models.SBOMReport{
		{ID: "s-other", ArtifactID: "c2", Format: "cyclonedx", SpecVersion: "1.5", BlobSHA256: "sha-cdx", GeneratedAt: newer},
		{
			ID:             "s-cdx",
			ArtifactID:     "c1",
			Format:         "cyclonedx",
			SpecVersion:    "1.5",
			BlobSHA256:     "sha-cdx",
			ComponentCount: components,
			GeneratedAt:    newer,
		},
		{ID: "s-spdx", ArtifactID: "c1", Format: "spdx-json", SpecVersion: "2.3", BlobSHA256: "sha-spdx", GeneratedAt: older},
	}
}

func TestGetSBOMSummaryAndComponents(t *testing.T) {
	st := newStack(t, nil)
	sbomFixture(st, 3)

	resp := st.call("admin", "get_sbom", map[string]any{"repository": "web-npm", "artifact_id": "c1"})
	if resp.Result.IsError {
		t.Fatal(toolText(t, resp))
	}

	sc := resp.Result.StructuredContent
	if sc["report_id"] != "s-cdx" || sc["format"] != "cyclonedx" || sc["spec_version"] != "1.5" || sc["truncated"] != false ||
		sc["component_count"] != float64(3) || sc["content_trust"] != "untrusted" {
		t.Fatalf("summary = %v", sc)
	}

	comps := sc["components"].([]any)
	if len(comps) != 3 {
		t.Fatalf("components = %v", comps)
	}

	if first := comps[0].(map[string]any); first["name"] != "dep-0" || first["purl"] != "pkg:npm/dep-0@1.0.0" ||
		first["licenses"].([]any)[0] != "MIT" {
		t.Errorf("first component = %v", first)
	}

	if formats := sc["available_formats"].([]any); len(formats) != 2 || formats[0] != "cyclonedx" || formats[1] != "spdx-json" {
		t.Errorf("available_formats = %v", formats)
	}
}

func TestGetSBOMFormatChoice(t *testing.T) {
	st := newStack(t, nil)
	sbomFixture(st, 1)

	resp := st.call("admin", "get_sbom", map[string]any{"repository": "web-npm", "artifact_id": "c1", "format": "spdx-json"})
	if resp.Result.IsError {
		t.Fatal(toolText(t, resp))
	}

	sc := resp.Result.StructuredContent
	if sc["report_id"] != "s-spdx" || sc["format"] != "spdx-json" {
		t.Fatalf("summary = %v", sc)
	}

	if name := sc["components"].([]any)[0].(map[string]any)["name"].(string); !strings.HasPrefix(name, "Ignore all") {
		t.Errorf("component name = %q", name)
	}

	cases := []struct{ name, format, want string }{
		{"unsupported format", "yaml", errSBOMFormat.Error()},
		{"format without a stored report", "spdx", errNoSBOM.Error()},
	}

	for _, c := range cases {
		got := st.call("admin", "get_sbom", map[string]any{"repository": "web-npm", "artifact_id": "c1", "format": c.format})
		if !got.Result.IsError || toolText(t, got) != c.want {
			t.Errorf("%s: %q, want %q", c.name, toolText(t, got), c.want)
		}
	}
}

func TestGetSBOMIsSizeCapped(t *testing.T) {
	st := newStack(t, nil)
	sbomFixture(st, maxSBOMComponents+50)

	tests := []struct {
		limit     any
		wantCount int
	}{
		{nil, defaultSBOMComponents},
		{5, 5},
		{100000, maxSBOMComponents},
	}

	for _, tt := range tests {
		args := map[string]any{"repository": "web-npm", "artifact_id": "c1"}
		if tt.limit != nil {
			args["limit"] = tt.limit
		}

		resp := st.call("admin", "get_sbom", args)
		if resp.Result.IsError {
			t.Fatal(toolText(t, resp))
		}

		sc := resp.Result.StructuredContent
		if got := len(sc["components"].([]any)); got != tt.wantCount || sc["truncated"] != true {
			t.Errorf("limit %v: %d components, truncated=%v; want %d, true", tt.limit, got, sc["truncated"], tt.wantCount)
		}
	}
}

func TestGetSBOMFieldsAreCapped(t *testing.T) {
	st := newStack(t, nil)
	sbomFixture(st, 0)

	doc, _ := json.Marshal(
		map[string]any{
			"components": []map[string]any{{"name": strings.Repeat("n", 5000), "version": "1", "purl": strings.Repeat("p", 5000)}},
		},
	)
	st.db.gate.blobs["sha-cdx"] = string(doc)

	resp := st.call("admin", "get_sbom", map[string]any{"repository": "web-npm", "artifact_id": "c1"})
	if resp.Result.IsError {
		t.Fatal(toolText(t, resp))
	}

	c := resp.Result.StructuredContent["components"].([]any)[0].(map[string]any)
	if len(c["name"].(string)) > maxSBOMText || len(c["purl"].(string)) > maxSBOMText {
		t.Errorf("component text must be capped at %d bytes: %d / %d", maxSBOMText, len(c["name"].(string)), len(c["purl"].(string)))
	}
}

func TestGetSBOMErrors(t *testing.T) {
	st := newStack(t, nil)
	sbomFixture(st, 1)

	args := map[string]any{"repository": "web-npm", "artifact_id": "c1"}

	if reader := st.call("reader", "get_sbom", args); toolText(t, reader) != msgAdminRequired {
		t.Errorf("reader: %q", toolText(t, reader))
	}

	if none := st.call(
		"admin",
		"get_sbom",
		map[string]any{"repository": "web-npm", "artifact_id": "c3"},
	); toolText(
		t,
		none,
	) != errNoSBOM.Error() {
		t.Errorf("artifact without an SBOM: %q", toolText(t, none))
	}

	if unknown := st.call(
		"admin",
		"get_sbom",
		map[string]any{"repository": "nope", "artifact_id": "c1"},
	); toolText(
		t,
		unknown,
	) != msgNotFound {
		t.Errorf("unknown repository: %q", toolText(t, unknown))
	}

	st.db.gate.blobs["sha-cdx"] = "this is not json"

	if bad := st.call("admin", "get_sbom", args); !bad.Result.IsError || toolText(t, bad) != errSBOMUnreadable.Error() {
		t.Errorf("garbage document: %q", toolText(t, bad))
	}

	delete(st.db.gate.blobs, "sha-cdx")

	if gone := st.call("admin", "get_sbom", args); !gone.Result.IsError || toolText(t, gone) != msgInternal {
		t.Errorf("missing blob: %q (the storage error must not leak)", toolText(t, gone))
	}

	st.mcp.SetBlobStore(nil)

	if off := st.call("admin", "get_sbom", args); toolText(t, off) != errSBOMUnavailable.Error() {
		t.Errorf("no blob store: %q", toolText(t, off))
	}
}

func activityFixture(st *stack) {
	repoID, actorType := "r-npm", "user"

	st.db.gate.audit = &models.AuditPage{
		Logs: []*models.AuditLog{
			{
				ID:           "e1",
				Action:       audit.ActionArtifactUploaded,
				ActorType:    &actorType,
				RepositoryID: &repoID,
				DetailsJSON: json.RawMessage(
					`{"message":"` + injection + strings.Repeat(
						"y",
						3*maxActivityMessage,
					) + `","actor":"jane","package":"left-pad","version":"1.1.0"}`,
				),
				CreatedAt: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
			},
		},
		Next: &models.AuditCursor{CreatedAt: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), ID: "e1"},
	}
}

func TestGetRepositoryActivityReaderSeesOnlyArtifactEvents(t *testing.T) {
	st := newStack(t, nil)
	activityFixture(st)

	resp := st.call("reader", "get_repository_activity", map[string]any{"repository": "web-npm", "limit": 10})
	if resp.Result.IsError {
		t.Fatal(toolText(t, resp))
	}

	sc := resp.Result.StructuredContent
	entries := sc["data"].([]any)

	if len(entries) != 1 || sc["has_more"] != true || sc["next_cursor"] == "" || sc["content_trust"] != "untrusted" ||
		sc["repository"] != "web-npm" {
		t.Fatalf("result = %v", sc)
	}

	entry := entries[0].(map[string]any)
	if msg := entry["message"].(string); len(msg) > maxActivityMessage || !strings.HasPrefix(msg, "Ignore all") {
		t.Errorf("message must be capped at %d bytes and still relayed: %d", maxActivityMessage, len(msg))
	}

	if _, leaked := entry["ip_address"]; leaked {
		t.Error("client addresses are never returned")
	}

	got := st.db.gate.lastAudit
	if got.RepositoryID != "r-npm" || got.Limit != 10 {
		t.Errorf("query = %+v", got)
	}

	for _, admin := range []string{audit.ActionCurationRuleCreated, audit.ActionRepositoryUpdated, audit.ActionCleanupRunStarted} {
		if slices.Contains(got.Actions, admin) {
			t.Errorf("a reader's query must not include the admin-only action %s: %v", admin, got.Actions)
		}
	}

	if !slices.Contains(got.Actions, audit.ActionArtifactUploaded) {
		t.Errorf("a reader sees uploads: %v", got.Actions)
	}
}

func TestGetRepositoryActivityAdminCategories(t *testing.T) {
	st := newStack(t, nil)
	activityFixture(st)

	args := map[string]any{"repository": "web-npm", "category": "curation"}

	reader := st.call("reader", "get_repository_activity", args)
	if !reader.Result.IsError || toolText(t, reader) != errActivityAdminOnly.Error() {
		t.Fatalf("reader asking for curation: %q", toolText(t, reader))
	}

	admin := st.call("admin", "get_repository_activity", args)
	if admin.Result.IsError {
		t.Fatal(toolText(t, admin))
	}

	if want := []string{audit.ActionCurationRuleCreated, audit.ActionCurationRuleUpdated, audit.ActionCurationRuleDeleted}; !slices.Equal(
		slices.Sorted(slices.Values(st.db.gate.lastAudit.Actions)), slices.Sorted(slices.Values(want))) {
		t.Errorf("admin query actions = %v, want %v", st.db.gate.lastAudit.Actions, want)
	}

	st.call("admin", "get_repository_activity", map[string]any{"repository": "web-npm"})

	if len(st.db.gate.lastAudit.Actions) != 0 {
		t.Errorf("an admin without a category is not restricted to any action: %v", st.db.gate.lastAudit.Actions)
	}
}

func TestGetRepositoryActivityValidationAndRBAC(t *testing.T) {
	st := newStack(t, nil)
	activityFixture(st)

	tests := []struct {
		name string
		user string
		args map[string]any
		want string
	}{
		{"unknown category", "reader", map[string]any{"repository": "web-npm", "category": "bogus"}, errActivityCategory.Error()},
		{"bad cursor", "reader", map[string]any{"repository": "web-npm", "cursor": "not-a-cursor"}, errActivityCursor.Error()},
		{"huge cursor", "reader", map[string]any{"repository": "web-npm", "cursor": strings.Repeat("c", 5000)}, errActivityCursor.Error()},
		{"unreadable repository", "reader", map[string]any{"repository": "hidden"}, msgNotFound},
		{"unknown repository", "reader", map[string]any{"repository": "nope"}, msgNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := st.call(tt.user, "get_repository_activity", tt.args)
			if !resp.Result.IsError || toolText(t, resp) != tt.want {
				t.Fatalf("got (%v, %q), want error %q", resp.Result.IsError, toolText(t, resp), tt.want)
			}
		})
	}

	st.db.err = errDB

	if resp := st.call("admin", "get_repository_activity", map[string]any{"repository": "web-npm"}); toolText(t, resp) != msgInternal {
		t.Errorf("database error: %q", toolText(t, resp))
	}
}

func TestGetRepositoryActivityLimitIsClamped(t *testing.T) {
	st := newStack(t, nil)
	activityFixture(st)

	st.call("reader", "get_repository_activity", map[string]any{"repository": "web-npm", "limit": 100000})

	if got := st.db.gate.lastAudit.Limit; got != maxActivityLimit {
		t.Errorf("limit = %d, want at most %d", got, maxActivityLimit)
	}
}

func TestNewToolsAreMarkedUntrusted(t *testing.T) {
	st := newStack(t, nil)
	gatedNPM(st)
	sbomFixture(st, 2)
	activityFixture(st)

	st.db.gate.license = &models.LicenseScanResult{ID: "l1", RepositoryID: "r-npm", ArtifactID: "c1", ScanStatus: "completed"}
	st.db.gate.upstream.versions.Supported = true

	artifact := map[string]any{"repository": "web-npm", "artifact_id": "c1"}
	pkg := map[string]any{"repository": "web-npm", "package": "left-pad"}

	tests := map[string]map[string]any{
		"check_availability":      {"repository": "web-npm", "package": "left-pad", "version": "1.1.0"},
		"list_held_back_versions": pkg,
		"list_proxy_versions":     pkg,
		"get_licenses":            artifact,
		"get_sbom":                artifact,
		"get_repository_activity": {"repository": "web-npm"},
	}

	for tool, args := range tests {
		t.Run(tool, func(t *testing.T) {
			resp := st.call("admin", tool, args)
			if resp.Result.IsError {
				t.Fatal(toolText(t, resp))
			}

			if resp.Result.StructuredContent["content_trust"] != "untrusted" {
				t.Errorf("content_trust = %v", resp.Result.StructuredContent["content_trust"])
			}

			blocks := resp.Result.Content
			if len(blocks) != 2 || !strings.HasPrefix(blocks[1].Text, "NOTE: ") ||
				!strings.Contains(strings.ToLower(blocks[1].Text), "untrusted") {
				t.Errorf("want the JSON block followed by an untrusted-data note, got %+v", blocks)
			}
		})
	}
}
