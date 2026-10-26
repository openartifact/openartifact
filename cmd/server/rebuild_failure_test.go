// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/recovery"
)

func TestRunRebuildStartupFailures(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		yaml     string
		wantExit int // 0: a plain error, not an exitError
		want     string
	}{
		{name: "malformed config", yaml: "server: [unclosed\n", want: "failed to load configuration"},
		{name: "invalid config", env: map[string]string{"SERVER_PORT": "0"}, want: "invalid configuration"},
		{name: "short secret key", env: map[string]string{"OPENARTIFACT_SECRET_KEY": "short"}, want: "invalid OPENARTIFACT_SECRET_KEY"},
		{
			name:     "unknown storage backend aborts before touching the database",
			env:      map[string]string{"STORAGE_TYPE": "tape", "DB_HOST": "127.0.0.1", "DB_PORT": "1", "DB_CONNECT_TIMEOUT": "0"},
			wantExit: recovery.ExitFailed,
			want:     "open blob store",
		},
		{
			name:     "unreachable database",
			env:      map[string]string{"STORAGE_PATH": "", "DB_HOST": "127.0.0.1", "DB_PORT": "1", "DB_SSLMODE": "disable", "DB_CONNECT_TIMEOUT": "0"},
			wantExit: recovery.ExitFailed,
			want:     "connect to database",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetStartupEnv(t)

			for k, v := range tc.env {
				if k == "STORAGE_PATH" {
					v = t.TempDir()
				}

				t.Setenv(k, v)
			}

			args := []string{}
			if tc.yaml != "" {
				args = append(args, "--config", writeFile(t, "cfg.yaml", tc.yaml))
			}

			err := run(append([]string{"rebuild"}, args...))
			if err == nil {
				t.Fatal("rebuild succeeded, want an error")
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}

			var ee exitError

			isExit := errors.As(err, &ee)
			if tc.wantExit != 0 && (!isExit || ee.code != tc.wantExit) {
				t.Errorf("err = %v, want an exitError with code %d", err, tc.wantExit)
			}

			if tc.wantExit == 0 && isExit {
				t.Errorf("a startup error before the rebuild began must not claim a rebuild exit code: %v", err)
			}
		})
	}
}

func TestRunRebuildRejectsPositionalArguments(t *testing.T) {
	if err := run([]string{"rebuild", "extra"}); !errors.Is(err, errRebuildUsage) {
		t.Fatalf("err = %v, want errRebuildUsage", err)
	}
}

func TestRunRebuildHelpIsNotAFailure(t *testing.T) {
	if err := run([]string{"rebuild", "-h"}); err != nil {
		t.Fatalf("rebuild -h: %v", err)
	}
}

func TestMetricsAuthMiddleware(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		anonymous bool
		header    string
		want      int
	}{
		{"no token, anonymous not enabled: closed", "", false, "", http.StatusUnauthorized},
		{"no token: a bearer header does not open it", "", false, "Bearer anything", http.StatusUnauthorized},
		{"no token, anonymous enabled", "", true, "", http.StatusOK},
		{"token, no header", "s3cr3t", false, "", http.StatusUnauthorized},
		{"token, wrong value", "s3cr3t", false, "Bearer nope", http.StatusUnauthorized},
		{"token, empty bearer", "s3cr3t", false, "Bearer ", http.StatusUnauthorized},
		{"token, wrong scheme", "s3cr3t", false, "Basic s3cr3t", http.StatusUnauthorized},
		{"token, prefix of the real one", "s3cr3t", false, "Bearer s3cr3", http.StatusUnauthorized},
		{"token, correct", "s3cr3t", false, "Bearer s3cr3t", http.StatusOK},
		{"token set: anonymous flag does not bypass it", "s3cr3t", true, "", http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := metricsAuthMiddleware(tc.token, tc.anonymous)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
