// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestAPIText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "List repositories", "List repositories"},
		{"whitespace", "a\n  b\t c", "a b c"},
		{"angle tag", "setup guide: <url>; done", "setup guide: `<url>`; done"},
		{"already fenced", "use `<name>` here", "use `<name>` here"},
		{"lone angle", "a < b", "a `<` b"},
		{"mustache", "echo {{ .Name }} now", "echo `{{ .Name }}` now"},
		{"lone mustache", "open {{ only", "open `{{` only"},
		{"single braces untouched", "/api/v1/{id}", "/api/v1/{id}"},
		{"stray backtick", "a ` b", "a \\` b"},
		{"heading marker", "# not a heading", `\# not a heading`},
		{"list marker", "- not a list", `\- not a list`},
		{"numbered marker", "1. not a list", `1\. not a list`},
		{"empty", "  ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := apiText(tt.in); got != tt.want {
				t.Errorf("apiText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAPISlug(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"Repositories":     "repositories",
		"Dependency-Track": "dependency-track",
		"  SBOM / VEX  ":   "sbom-vex",
		"A__b":             "a-b",
		"???":              "",
	}

	for in, want := range tests {
		if got := apiSlug(in); got != want {
			t.Errorf("apiSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAPIStatusOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b string
		want bool
	}{
		{"200", "404", true},
		{"404", "200", false},
		{"99", "200", true},
		{"500", "default", true},
		{"default", "200", false},
		{"default", "default", false},
	}

	for _, tt := range tests {
		if got := apiStatusLess(tt.a, tt.b); got != tt.want {
			t.Errorf("apiStatusLess(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestAPIValue(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		`"abc"`:             "`abc`",
		`12`:                "`12`",
		`true`:              "`true`",
		`null`:              "`null`",
		`"a|b"`:             "`a|b`",
		`{ "a": 1 }`:        "`{\"a\":1}`",
		`"has ` + "`" + `"`: "`has '`",
	}

	for in, want := range tests {
		if got := apiValue([]byte(in)); got != want {
			t.Errorf("apiValue(%s) = %q, want %q", in, got, want)
		}
	}
}

// apiTestSpec wraps paths and schemas JSON into a minimal valid document.
func apiTestSpec(paths, schemas string) []byte {
	return []byte(`{"openapi":"3.0.3","info":{"title":"t"},"paths":{` + paths + `},"components":{"schemas":{` + schemas + `}}}`)
}

func TestAPIParseErrors(t *testing.T) {
	t.Parallel()

	const okOp = `"get":{"tags":["T"],"responses":{"200":{"description":"ok"}}}`

	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{"not json", []byte("{"), "parse"},
		{"version", []byte(`{"openapi":"2.0"}`), "unsupported openapi"},
		{"no operations", apiTestSpec("", ""), "no operations"},
		{"no responses", apiTestSpec(`"/a":{"get":{"tags":["T"],"responses":{}}}`, ""), "no responses"},
		{
			"unresolved ref in response",
			apiTestSpec(`"/a":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Nope"}}}}}}}`, ""),
			"unresolved $ref",
		},
		{
			"unresolved ref in schema",
			apiTestSpec(`"/a":{`+okOp+`}`, `"A":{"type":"object","properties":{"b":{"$ref":"#/components/schemas/Nope"}}}`),
			"unresolved $ref",
		},
		{
			"foreign ref",
			apiTestSpec(`"/a":{`+okOp+`}`, `"A":{"type":"array","items":{"$ref":"other.json#/X"}}`),
			"unresolved $ref",
		},
		{"schema name", apiTestSpec(`"/a":{`+okOp+`}`, `"Foo_Bar":{"type":"string"}`), "letters and digits"},
		{"schema anchor collision", apiTestSpec(`"/a":{`+okOp+`}`, `"Foo":{"type":"string"},"FOO":{"type":"string"}`), "same anchor"},
		{"schema reserved anchor", apiTestSpec(`"/a":{`+okOp+`}`, `"Schemas":{"type":"string"}`), "same anchor"},
		{"path with tag", apiTestSpec(`"/a/<b>":{`+okOp+`}`, ""), "break markdown"},
		{"parameter ref", apiTestSpec(`"/a":{"get":{"parameters":[{"$ref":"#/components/parameters/P"}],"responses":{"200":{"description":"ok"}}}}`, ""), "parameter $ref"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := apiParse(tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("apiParse error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestAPITagPagesErrors(t *testing.T) {
	t.Parallel()

	op := func(tag string) string {
		return `"get":{"tags":["` + tag + `"],"responses":{"200":{"description":"ok"}}}`
	}

	tests := []struct {
		name    string
		paths   string
		wantErr string
	}{
		{"same slug", `"/a":{` + op("Foo Bar") + `},"/b":{` + op("foo-bar") + `}`, "already used"},
		{"reserved index", `"/a":{` + op("README") + `}`, "already used"},
		{"reserved schemas", `"/a":{` + op("Schemas") + `}`, "already used"},
		{"empty slug", `"/a":{` + op("???") + `}`, "no letters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doc, err := apiParse(apiTestSpec(tt.paths, ""))
			if err != nil {
				t.Fatal(err)
			}

			if _, err := doc.render(); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("render error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

const apiSampleSpec = `{
  "openapi": "3.0.3",
  "info": {"title": "Sample", "description": "Uses <angle> and {{mustache}}."},
  "servers": [{"url": "/", "description": "Current server"}],
  "security": [{"bearerAuth": []}, {"cookieAuth": []}],
  "tags": [{"name": "Zeta", "description": "Declared first, listed first."}],
  "paths": {
    "/b": {
      "post": {
        "tags": ["Alpha"], "summary": "Create", "description": "Needs <role> admin.",
        "parameters": [{"name": "dry", "in": "query", "schema": {"type": "boolean", "default": false}}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {
          "type": "object", "required": ["name"],
          "properties": {
            "name": {"type": "string", "enum": ["a|b", "c"]},
            "opts": {"type": "object", "properties": {"deep": {"type": "integer", "minimum": 1, "maximum": 5}}},
            "tags": {"type": "array", "items": {"$ref": "#/components/schemas/Node"}}
          }}}}},
        "responses": {"default": {"description": "error"}, "201": {"description": "created", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Node"}}}}}
      },
      "get": {"tags": ["Alpha"], "summary": "List", "security": [], "responses": {"200": {"description": "ok", "content": {"text/csv": {"schema": {"type": "string"}}}}}}
    },
    "/a/{id}": {
      "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string", "format": "uuid"}}],
      "delete": {"summary": "Remove", "deprecated": true, "responses": {"204": {"description": "gone"}}}
    }
  },
  "components": {
    "securitySchemes": {"bearerAuth": {"type": "http", "scheme": "bearer"}, "cookieAuth": {"type": "apiKey", "in": "cookie", "name": "session"}},
    "schemas": {
      "Node": {"type": "object", "required": ["id"], "properties": {
        "id": {"type": "string"},
        "parent": {"$ref": "#/components/schemas/Node"},
        "meta": {"type": "object", "additionalProperties": {"type": "string"}}
      }},
      "Child": {"allOf": [{"$ref": "#/components/schemas/Node"}, {"type": "object", "properties": {"extra": {"type": "string", "nullable": true}}}]},
      "Loop": {"allOf": [{"$ref": "#/components/schemas/Loop"}], "properties": {"x": {"type": "string"}}}
    }
  }
}`

func TestAPIRender(t *testing.T) {
	t.Parallel()

	doc, err := apiParse([]byte(apiSampleSpec))
	if err != nil {
		t.Fatal(err)
	}

	pages, err := doc.render()
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"api/README.md", "api/schemas.md", "api/alpha.md", "api/other.md"} {
		if _, ok := pages[name]; !ok {
			t.Errorf("missing page %s", name)
		}
	}

	if len(pages) != 4 {
		t.Errorf("got %d pages, want 4", len(pages))
	}

	// "Zeta" is declared but has no operation: it is not listed. Untagged goes to Other.
	if _, ok := pages["api/zeta.md"]; ok {
		t.Error("tag without operations got a page")
	}

	checks := map[string][]string{
		"api/alpha.md": {
			"### `GET /b`\n", "### `POST /b`\n", // GET before POST
			"**Authentication:** none (public endpoint).",
			"Needs `<role>` admin.",
			"| `dry` | query | boolean | no | Default: `false`. |",
			"**Request body** (required)",
			"| `name` | string | yes | One of: `a\\|b`, `c`. |",
			"| `opts.deep` | integer | no | Minimum: 1. Maximum: 5. |",
			"| `tags` | array of [Node](schemas.md#node) | no | - |",
			"| `201` | created | `application/json`: [Node](schemas.md#node) |",
			"| `default` | error | - |",
			"`text/csv`: string",
		},
		"api/other.md": {
			"### `DELETE /a/{id}`", "**Deprecated.**", "| `id` | path | string (uuid) | yes | - |",
		},
		"api/schemas.md": {
			"### Node\n", "### Child\n", "Includes all fields of [Node](#node).",
			"| `extra` | string | no | Nullable. |",
			"| `parent` | [Node](#node) | no | - |",
			"| `meta` | map of string | no | - |",
			"| `id` | string | yes | - |", // required carried through allOf
		},
		"api/README.md": {
			"`bearerAuth` or `cookieAuth`", "| `cookieAuth` | apiKey | `session` in cookie | - |",
			"- `/`: Current server", "[Alpha](alpha.md) | 2 |", "[Other](other.md) | 1 |",
			"../../integrations/api.md",
		},
	}

	for name, wants := range checks {
		for _, want := range wants {
			if !strings.Contains(string(pages[name]), want) {
				t.Errorf("%s: missing %q", name, want)
			}
		}
	}

	alpha := string(pages["api/alpha.md"])
	if strings.Index(alpha, "### `GET /b`") > strings.Index(alpha, "### `POST /b`") {
		t.Error("GET must come before POST for the same path")
	}

	assertNoRawMarkup(t, pages)
}

func TestAPIRenderDeclaredTagOrder(t *testing.T) {
	t.Parallel()

	spec := `{"openapi":"3.0.3","tags":[{"name":"Zeta","description":"Z <desc>"}],"paths":{
	"/a":{"get":{"tags":["alpha"],"responses":{"200":{"description":"ok"}}}},
	"/z":{"get":{"tags":["Zeta"],"responses":{"200":{"description":"ok"}}}},
	"/b":{"get":{"tags":["Beta"],"responses":{"200":{"description":"ok"}}}}}}`

	doc, err := apiParse([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}

	pages, err := doc.tagPages()
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, p := range pages {
		got = append(got, p.name)
	}

	if want := "Zeta,alpha,Beta"; strings.Join(got, ",") != want {
		t.Errorf("tag order = %v, want %s", got, want)
	}

	if pages[0].desc != "Z <desc>" {
		t.Errorf("tag description = %q", pages[0].desc)
	}
}

func TestAPIRenderDeterministic(t *testing.T) {
	t.Parallel()

	var first map[string][]byte

	for range 5 {
		doc, err := apiParse([]byte(apiSampleSpec))
		if err != nil {
			t.Fatal(err)
		}

		pages, err := doc.render()
		if err != nil {
			t.Fatal(err)
		}

		if first == nil {
			first = pages

			continue
		}

		for name, content := range pages {
			if string(first[name]) != string(content) {
				t.Fatalf("%s differs between runs", name)
			}
		}
	}
}

func TestAPIDepthLimit(t *testing.T) {
	t.Parallel()

	// Seven levels of inline nesting: expansion stops after apiMaxDepth.
	schema := `{"type":"string"}`
	for _, n := range []string{"g", "f", "e", "d", "c", "b", "a"} {
		schema = `{"type":"object","properties":{"` + n + `":` + schema + `}}`
	}

	doc, err := apiParse(apiTestSpec(`"/a":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":`+schema+`}}}}}}`, ""))
	if err != nil {
		t.Fatal(err)
	}

	pages, err := doc.render()
	if err != nil {
		t.Fatal(err)
	}

	other := string(pages["api/other.md"])
	if !strings.Contains(other, "`a.b.c.d`") {
		t.Error("expected fields down to the depth limit")
	}

	if strings.Contains(other, "`a.b.c.d.e.f`") {
		t.Error("expansion went past the depth limit")
	}
}

// assertNoRawMarkup checks the pages have no raw "<" or "{{" outside code
// spans, apart from the leading comments every page starts with.
func assertNoRawMarkup(t *testing.T, pages map[string][]byte) {
	t.Helper()

	comment := regexp.MustCompile(`(?s)<!--.*?-->`)
	span := regexp.MustCompile("`[^`\n]*`")

	for name, content := range pages {
		body := span.ReplaceAllString(comment.ReplaceAllString(string(content), ""), "")
		if strings.ContainsAny(body, "<") || strings.Contains(body, "{{") {
			t.Errorf("%s: raw markup outside code spans", name)
		}
	}
}

// TestAPIRealSpec renders the committed spec: it must parse, resolve every
// reference and produce pages free of raw markup.
func TestAPIRealSpec(t *testing.T) {
	t.Parallel()

	pages, err := genAPI("../..")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := pages["api/README.md"]; !ok {
		t.Fatal("no index page")
	}

	assertNoRawMarkup(t, pages)
}
