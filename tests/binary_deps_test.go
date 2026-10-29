// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// goListDeps returns the import paths one main package links, as the release
// builds see them (CGO off, the release build tags).
func goListDeps(t *testing.T, tags, pkg string) []string {
	t.Helper()

	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}

	args := []string{"list", "-deps"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}

	args = append(args, pkg)

	cmd := exec.CommandContext(t.Context(), gobin, args...)
	cmd.Dir = ".."
	cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0")

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go %s: %v", strings.Join(args, " "), err)
	}

	return strings.Fields(string(out))
}

// TestBinaryImportGraphs keeps the small binaries small: the CLI and the
// password-reset tool must not link the server stack, and the server must not
// link golang.org/x/net/trace (gRPC's debug tracing, dropped by the
// grpcnotrace build tag), which pulls html/template and makes the linker keep
// every exported method of every reachable type.
func TestBinaryImportGraphs(t *testing.T) {
	t.Parallel()

	const (
		module = "github.com/openartifact/openartifact/"
		// releaseTags are the build tags of the official server binary
		// (Makefile, docker/Dockerfile, .goreleaser.yml).
		releaseTags = "grpcnotrace"
	)

	serverOnly := []string{
		module + "internal/api",
		module + "internal/database",
		module + "internal/storage",
		module + "internal/telemetry",
		module + "internal/worker",
		module + "internal/vuln",
		module + "web",
		"go.opentelemetry.io",
		"google.golang.org/grpc",
		"github.com/aws",
		"github.com/prometheus",
		"github.com/modelcontextprotocol",
		"github.com/crewjam/saml",
	}

	// slimTags leave AI analysis and the MCP server out (docs/development/README.md#build-tags).
	const slimTags = releaseTags + ",noai,nomcp"

	// aiPkg is the LLM client package; internal/ai/manifest next to it is
	// shared with the always-built pkgdeps, licensemeta and sbom, so only this
	// exact path is forbidden in the slim server.
	const aiPkg = module + "internal/ai"

	tests := []struct {
		name      string
		pkg       string
		tags      string
		forbidden []string
		// forbiddenExact are import paths forbidden without their subpackages.
		forbiddenExact []string
		// required are import paths that must be linked, so a guard on a
		// feature cannot pass because the feature moved elsewhere.
		required []string
	}{
		{name: "oa", pkg: "./cmd/oa", forbidden: append([]string{"github.com/jackc/pgx"}, serverOnly...)},
		{name: "resetpw", pkg: "./cmd/resetpw", forbidden: serverOnly},
		{
			name: "server", pkg: "./cmd/server", tags: releaseTags,
			forbidden: []string{"golang.org/x/net/trace"},
			required:  []string{aiPkg, module + "internal/mcp", "github.com/modelcontextprotocol/go-sdk/mcp"},
		},
		{
			name: "server-slim", pkg: "./cmd/server", tags: slimTags,
			forbidden: []string{
				"golang.org/x/net/trace",
				module + "internal/mcp",
				"github.com/modelcontextprotocol",
				// Only the MCP SDK imports these.
				"github.com/google/jsonschema-go",
				"github.com/segmentio",
				"github.com/yosida95/uritemplate",
			},
			forbiddenExact: []string{aiPkg},
			required:       []string{aiPkg + "/manifest"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			linked := map[string]bool{}

			for _, dep := range goListDeps(t, tc.tags, tc.pkg) {
				linked[dep] = true

				for _, prefix := range tc.forbidden {
					if dep == prefix || strings.HasPrefix(dep, prefix+"/") {
						t.Errorf("%s links %s (forbidden prefix %q)", tc.pkg, dep, prefix)
					}
				}

				if slices.Contains(tc.forbiddenExact, dep) {
					t.Errorf("%s links %s (forbidden)", tc.pkg, dep)
				}
			}

			for _, dep := range tc.required {
				if !linked[dep] {
					t.Errorf("%s (tags %q) does not link %s", tc.pkg, tc.tags, dep)
				}
			}
		})
	}
}
