// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// JSON-RPC codes the SDK emits for protocol-level errors.
const jsonRPCInvalidParams = -32602

func allTools() []string {
	return []string{
		"client_setup", "get_artifact_analysis", "get_package", "get_repository", "get_vulnerabilities",
		"list_ai_analyses", "list_packages", "list_repositories", "search_artifacts", "trigger_ai_analysis",
	}
}

// bearerRoundTripper authenticates every request of an SDK client.
type bearerRoundTripper struct{ token string }

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)

	return http.DefaultTransport.RoundTrip(r)
}

// connect opens an MCP client session over a real HTTP server running the
// stack, negotiating the given protocol version ("" = the SDK's latest).
func (st *stack) connect(user, version string) (*sdk.ClientSession, error) {
	srv := httptest.NewServer(st.handler)
	st.t.Cleanup(srv.Close)

	client := sdk.NewClient(&sdk.Implementation{Name: "conformance-test", Version: "1"}, nil)

	session, err := client.Connect(st.t.Context(), &sdk.StreamableClientTransport{
		Endpoint:             srv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerRoundTripper{token: st.secrets[user]}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, &sdk.ClientSessionOptions{ProtocolVersion: version})
	if err == nil {
		st.t.Cleanup(func() { _ = session.Close() })
	}

	return session, err
}

func TestMCPNegotiatesEveryProtocolRevision(t *testing.T) {
	st := newStack(t, nil)

	for _, version := range []string{"", "2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26"} {
		t.Run("client "+version, func(t *testing.T) {
			session, err := st.connect("reader", version)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}

			tools, err := session.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatalf("tools/list: %v", err)
			}

			if len(tools.Tools) != len(allTools()) {
				t.Fatalf("got %d tools, want %d", len(tools.Tools), len(allTools()))
			}

			res, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "list_repositories", Arguments: map[string]any{}})
			if err != nil || res.IsError {
				t.Fatalf("tools/call: %v %+v", err, res)
			}
		})
	}
}

func TestMCPUnsupportedProtocolVersionHeaderIsRefused(t *testing.T) {
	st := newStack(t, nil)

	rec := st.do(http.MethodPost, "reader", rpcBody(t, 1, "tools/list", nil), map[string]string{"MCP-Protocol-Version": "1999-01-01"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestMCPInitializeReportsServerAndCapabilities(t *testing.T) {
	st := newStack(t, nil)

	session, err := st.connect("reader", "")
	if err != nil {
		t.Fatal(err)
	}

	init := session.InitializeResult()
	if init.ServerInfo.Name != "OpenArtifact" || init.ServerInfo.Version != "1.2.3" {
		t.Errorf("serverInfo = %+v", init.ServerInfo)
	}

	if init.Capabilities.Tools == nil || init.Capabilities.Resources != nil || init.Capabilities.Prompts != nil {
		t.Errorf("capabilities must advertise tools only: %+v", init.Capabilities)
	}

	if !strings.Contains(init.Instructions, "untrusted") {
		t.Errorf("instructions must warn about untrusted content: %q", init.Instructions)
	}
}

func TestMCPToolsAdvertiseAnnotationsAndOutputSchemas(t *testing.T) {
	st := newStack(t, nil)

	session, err := st.connect("reader", "")
	if err != nil {
		t.Fatal(err)
	}

	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var names []string

	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		assertToolMetadata(t, tool)
	}

	slices.Sort(names)

	if !slices.Equal(names, allTools()) {
		t.Fatalf("tools = %v, want %v", names, allTools())
	}
}

func assertToolMetadata(t *testing.T, tool *sdk.Tool) {
	t.Helper()

	a := tool.Annotations
	if a == nil || a.Title == "" {
		t.Errorf("%s: missing annotations or title", tool.Name)
		return
	}

	if tool.Description == "" {
		t.Errorf("%s: missing description", tool.Name)
	}

	// idempotentHint and openWorldHint travel as explicit values too; a missing
	// hint would fall back to the spec's pessimistic defaults.
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}

	var wire struct {
		Annotations map[string]any `json:"annotations"`
		Output      map[string]any `json:"outputSchema"` //nolint:tagliatelle // MCP wire format is camelCase
		Input       map[string]any `json:"inputSchema"`  //nolint:tagliatelle // MCP wire format is camelCase
	}

	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}

	for _, hint := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
		if _, ok := wire.Annotations[hint]; !ok {
			t.Errorf("%s: annotation %s not advertised", tool.Name, hint)
		}
	}

	if wire.Output["type"] != "object" || wire.Output["properties"] == nil {
		t.Errorf("%s: outputSchema = %v, want an object schema with properties", tool.Name, wire.Output)
	}

	if wire.Input["type"] != "object" {
		t.Errorf("%s: inputSchema = %v", tool.Name, wire.Input)
	}

	wantReadOnly := tool.Name != "trigger_ai_analysis"
	if a.ReadOnlyHint != wantReadOnly {
		t.Errorf("%s: readOnlyHint = %v, want %v", tool.Name, a.ReadOnlyHint, wantReadOnly)
	}

	if a.DestructiveHint == nil || *a.DestructiveHint {
		t.Errorf("%s: destructiveHint must be explicitly false", tool.Name)
	}

	if wantReadOnly && !a.IdempotentHint {
		t.Errorf("%s: read tools are idempotent", tool.Name)
	}

	if !wantReadOnly && (a.IdempotentHint || a.OpenWorldHint == nil || !*a.OpenWorldHint) {
		t.Errorf("%s: trigger must be non-idempotent and open-world: %+v", tool.Name, a)
	}
}

func TestMCPProtocolErrorsUseJSONRPCCodes(t *testing.T) {
	st := newStack(t, nil)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"unknown tool", rpcBody(t, 1, "tools/call", map[string]any{"name": "drop_database"}), jsonRPCInvalidParams},
		{"tools/call without a tool name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{}}}`, jsonRPCInvalidParams},
		{"tools/call params of the wrong type", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"x"}`, jsonRPCInvalidParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := decodeToolResponse(t, st.do(http.MethodPost, "reader", tt.body, nil))
			if resp.Error == nil || resp.Error.Code != tt.want {
				t.Fatalf("want JSON-RPC error %d, got %+v", tt.want, resp.Error)
			}
		})
	}
}

func TestMCPInvalidArgumentsAreToolErrors(t *testing.T) {
	// MCP 2026-07-28 (SEP-1303): input validation failures are tool execution
	// errors the model can read and correct, not protocol errors.
	st := newStack(t, nil)

	tests := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"missing required argument", "get_repository", map[string]any{}},
		{"wrong argument type", "list_packages", map[string]any{"repository": "web-npm", "page": "two"}},
		{"unknown argument", "get_repository", map[string]any{"name": "web-npm", "admin": true}},
		{"invalid status filter", "list_ai_analyses", map[string]any{"repository_id": "r-npm", "status": "exploded"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := st.call("reader", tt.tool, tt.args)
			if resp.Error != nil || !resp.Result.IsError || toolText(t, resp) == "" {
				t.Fatalf("want a readable tool error, got %+v / %+v", resp.Error, resp.Result)
			}
		})
	}
}
