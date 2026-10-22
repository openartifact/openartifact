// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runAsServerEnv makes the re-executed test binary behave like the real server
// binary: TestMain hands control to main() instead of running the tests.
const runAsServerEnv = "OA_TEST_RUN_AS_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(runAsServerEnv) == "1" {
		main()
		os.Exit(0)
	}

	os.Exit(m.Run())
}

// unreachableDBEnv returns an environment for a server subprocess that fails at the
// database: nothing listens on port 1, so the connection is refused at once.
func unreachableDBEnv(logPath string) []string {
	return []string{
		"DB_HOST=127.0.0.1",
		"DB_PORT=1",
		"DB_SSLMODE=disable",
		"LOG_FORMAT=json",
		"LOG_OUTPUT=" + logPath,
		"OPENARTIFACT_SECRET_KEY=",
		"OPENARTIFACT_CONFIG=",
	}
}

// execServer runs main() of a fresh process with args and env and returns its
// exit code and combined stdout/stderr.
func execServer(t *testing.T, env []string, args ...string) (int, string) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), os.Args[0], args...)
	cmd.Env = append(os.Environ(), append([]string{runAsServerEnv + "=1"}, env...)...)

	var out bytes.Buffer

	cmd.Stdout, cmd.Stderr = &out, &out

	err := cmd.Run()
	if err == nil {
		return 0, out.String()
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run server subprocess: %v", err)
	}

	return exitErr.ExitCode(), out.String()
}

// TestStartupFailureExitsNonZero is the regression test for a server that
// logged a startup error and then returned from main(), i.e. exited 0 and let
// an orchestrator believe it had run to completion.
func TestStartupFailureExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	badYAML := filepath.Join(dir, "bad.yaml")

	if err := os.WriteFile(badYAML, []byte("server: [not, a, mapping\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		env      []string
		args     []string
		wantCode int
		wantOut  string // substring of the combined output; checked in logFile when set
		inLog    bool
	}{
		{
			name:     "unreachable database",
			env:      unreachableDBEnv(filepath.Join(dir, "db.log")),
			wantCode: 1,
			wantOut:  "failed to connect to database",
			inLog:    true,
		},
		{
			name:     "unparsable config file",
			env:      unreachableDBEnv(filepath.Join(dir, "cfg.log")),
			args:     []string{"-config", badYAML},
			wantCode: 1,
			wantOut:  "failed to load configuration",
		},
		{
			name:     "invalid configuration value",
			env:      append(unreachableDBEnv(filepath.Join(dir, "val.log")), "SERVER_PORT=70000"),
			wantCode: 1,
			wantOut:  "invalid configuration",
		},
		{
			name:     "unknown flag",
			env:      unreachableDBEnv(filepath.Join(dir, "flag.log")),
			args:     []string{"-no-such-flag"},
			wantCode: 1,
			wantOut:  "flag provided but not defined",
		},
		{
			name:     "migrate flag against unreachable database",
			env:      unreachableDBEnv(filepath.Join(dir, "mig.log")),
			args:     []string{"-migrate"},
			wantCode: 1,
			wantOut:  "failed to connect to database",
			inLog:    true,
		},
		{
			name:     "verify rejects a broken config",
			env:      unreachableDBEnv(filepath.Join(dir, "ver.log")),
			args:     []string{"verify", badYAML},
			wantCode: 1,
			wantOut:  "config validation failed",
		},
		{
			name:     "verify without a path",
			env:      unreachableDBEnv(filepath.Join(dir, "ver2.log")),
			args:     []string{"verify"},
			wantCode: 1,
			wantOut:  "usage: openartifact-server verify",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := execServer(t, tt.env, tt.args...)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d\noutput:\n%s", code, tt.wantCode, out)
			}

			haystack := out

			if tt.inLog {
				var logPath string

				for _, kv := range tt.env {
					if v, ok := strings.CutPrefix(kv, "LOG_OUTPUT="); ok {
						logPath = v
					}
				}

				b, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatalf("read log file: %v", err)
				}

				haystack = string(b)
			}

			if !strings.Contains(haystack, tt.wantOut) {
				t.Errorf("output does not contain %q:\n%s", tt.wantOut, haystack)
			}
		})
	}
}

// TestCleanExitsStayZero guards the other direction: the paths that are not
// failures (valid verify, -h) must keep exiting 0.
func TestCleanExitsStayZero(t *testing.T) {
	dir := t.TempDir()
	okYAML := filepath.Join(dir, "ok.yaml")

	if err := os.WriteFile(okYAML, []byte("server:\n  port: 8081\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	env := unreachableDBEnv(filepath.Join(dir, "ok.log"))

	if code, out := execServer(t, env, "verify", okYAML); code != 0 {
		t.Errorf("verify valid config: exit %d, want 0\n%s", code, out)
	}

	if code, out := execServer(t, env, "-h"); code != 0 {
		t.Errorf("-h: exit %d, want 0\n%s", code, out)
	}
}

// TestVersionFlag checks that -version prints the build metadata and exits 0
// without touching the configuration or the database (the DB is unreachable here).
func TestVersionFlag(t *testing.T) {
	env := unreachableDBEnv(filepath.Join(t.TempDir(), "version.log"))

	code, out := execServer(t, env, "-version")
	if code != 0 {
		t.Fatalf("-version: exit %d, want 0\n%s", code, out)
	}

	fields := strings.Fields(out)
	if len(fields) != 4 {
		t.Fatalf("-version output %q: want 4 fields (version commit build-time go/os/arch)", out)
	}

	if fields[0] != version || fields[1] != commit || fields[2] != buildTime {
		t.Errorf("-version output %q does not match the stamped metadata %s %s %s", out, version, commit, buildTime)
	}

	if !strings.HasPrefix(fields[3], "go") || strings.Count(fields[3], "/") != 2 {
		t.Errorf("-version runtime field %q: want go<ver>/<os>/<arch>", fields[3])
	}
}
