// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"os/exec"
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

	tests := []struct {
		name      string
		pkg       string
		tags      string
		forbidden []string
	}{
		{"oa", "./cmd/oa", "", append([]string{"github.com/jackc/pgx"}, serverOnly...)},
		{"resetpw", "./cmd/resetpw", "", serverOnly},
		{"server", "./cmd/server", releaseTags, []string{"golang.org/x/net/trace"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			for _, dep := range goListDeps(t, tc.tags, tc.pkg) {
				for _, prefix := range tc.forbidden {
					if dep == prefix || strings.HasPrefix(dep, prefix+"/") {
						t.Errorf("%s links %s (forbidden prefix %q)", tc.pkg, dep, prefix)
					}
				}
			}
		})
	}
}
