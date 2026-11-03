// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"os/exec"
	"path/filepath"
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

// TestBinaryImportGraphs keeps the small binaries small: the CLI (cmd/oa, the
// package internal/oacli that the server also embeds) and the
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
		// allowedExternal, when set, is the complete list of third-party
		// import path prefixes the binary may link: a new dependency of the
		// CLI is a decision, not a side effect of an import.
		allowedExternal []string
	}{
		{
			name: "oa", pkg: "./cmd/oa", forbidden: append([]string{"github.com/jackc/pgx"}, serverOnly...),
			allowedExternal: []string{
				// the command tree (mousetrap is cobra's Windows-only dependency)
				"github.com/spf13/cobra", "github.com/spf13/pflag", "github.com/inconshreveable/mousetrap",
				"go.yaml.in/yaml", "golang.org/x/sync", "golang.org/x/mod", "golang.org/x/sys", "golang.org/x/term",
				"github.com/pelletier/go-toml",
			},
		},
		{name: "resetpw", pkg: "./cmd/resetpw", forbidden: serverOnly},
		{
			name: "server", pkg: "./cmd/server", tags: releaseTags,
			forbidden: []string{"golang.org/x/net/trace"},
			// internal/oacli: the server answers as `oa`, so the image needs no second binary.
			required: []string{aiPkg, module + "internal/mcp", "github.com/modelcontextprotocol/go-sdk/mcp", module + "internal/oacli"},
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
			required:       []string{aiPkg + "/manifest", module + "internal/oacli"},
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

				if tc.allowedExternal != nil && isThirdParty(dep) && !hasPrefixIn(dep, tc.allowedExternal) {
					t.Errorf("%s links %s, which is not in its allowed third-party list", tc.pkg, dep)
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

// linkedSymbols builds one main package like the release (CGO off, -trimpath,
// symbol table kept) and returns the symbols of the binary, one per line.
func linkedSymbols(t *testing.T, tags, pkg string) string {
	t.Helper()

	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}

	bin := filepath.Join(t.TempDir(), "main")
	args := []string{"build", "-trimpath", "-ldflags", "-w", "-o", bin}

	if tags != "" {
		args = append(args, "-tags", tags)
	}

	args = append(args, pkg)

	build := exec.CommandContext(t.Context(), gobin, args...)
	build.Dir = ".."
	build.Env = append(build.Environ(), "CGO_ENABLED=0")

	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}

	nm := exec.CommandContext(t.Context(), gobin, "tool", "nm", bin)

	out, err := nm.Output()
	if err != nil {
		t.Fatalf("go tool nm: %v", err)
	}

	return string(out)
}

// TestBinariesDoNotExecuteTemplates keeps text/template execution out of oa
// and the server, which embeds the CLI (`openartifact oa`). Execution calls
// reflect.Value.MethodByName with a run-time name, which switches the linker
// to keep every exported method of every reachable type: about 1.9 MB of the
// CLI, and more of the server. Cobra reaches it only through SetHelpTemplate,
// SetUsageTemplate and SetVersionTemplate (its default output is written in
// Go), so a command that needs custom output uses SetHelpFunc or SetUsageFunc.
func TestBinariesDoNotExecuteTemplates(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("builds the binaries")
	}

	for _, tc := range []struct{ name, tags, pkg string }{
		{"oa", "", "./cmd/oa"},
		{"server", "grpcnotrace", "./cmd/server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			symbols := linkedSymbols(t, tc.tags, tc.pkg)

			for _, sym := range []string{
				"text/template.(*Template).Execute",
				"text/template.(*state).evalField",
			} {
				if strings.Contains(symbols, " "+sym+"\n") {
					t.Errorf("%s links %s: something sets a cobra help, usage or version template, or executes a text/template", tc.pkg, sym)
				}
			}
		})
	}
}

// isThirdParty reports whether an import path is outside the standard library
// (its first element has a dot; the vendored copies of x/ packages that the
// standard library carries live under vendor/) and outside this module.
func isThirdParty(dep string) bool {
	first, _, _ := strings.Cut(dep, "/")

	return strings.Contains(first, ".") && !strings.HasPrefix(dep, "github.com/openartifact/openartifact")
}

func hasPrefixIn(dep string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(p string) bool { return dep == p || strings.HasPrefix(dep, p+"/") })
}
