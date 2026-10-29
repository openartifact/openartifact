// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/bgtask"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/workq"
)

func roleApp(t *testing.T, role string) *app {
	t.Helper()

	cfg := config.Defaults()
	cfg.Server.Role = role

	a, _ := newTestApp(t, cfg)
	a.tasks = bgtask.New(context.Background())

	t.Cleanup(func() { a.tasks.Shutdown(backgroundDrainTimeout) })

	return a
}

func TestStartRunsTheWorkerOnlyWhereTheWorkersRun(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{config.RoleAll, true},
		{config.RoleWorker, true},
		{config.RoleWeb, false},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			a := roleApp(t, tt.role)

			started := false
			a.start(func() { started = true })

			if started != tt.want {
				t.Errorf("start ran the worker = %v, want %v", started, tt.want)
			}

			ran := false
			a.background(func(context.Context) { ran = true })

			// Shutdown waits for the task, so reading ran afterwards is ordered.
			a.tasks.Shutdown(backgroundDrainTimeout)

			if ran != tt.want {
				t.Errorf("background ran the task = %v, want %v", ran, tt.want)
			}
		})
	}
}

// A worker process answers the operational endpoints and nothing else.
func TestWorkerServesOnlyTheOperationalRoutes(t *testing.T) {
	t.Setenv("METRICS_AUTH_TOKEN", "s3cret")
	t.Cleanup(func() {
		runtime.SetMutexProfileFraction(0)
		runtime.SetBlockProfileRate(0)
	})

	a := roleApp(t, config.RoleWorker)
	a.cfg.Server.PprofEnabled = true
	a.web.router = chi.NewRouter()
	a.registerOperationalRoutes()

	get := func(path, token string) int {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		rw := httptest.NewRecorder()
		a.web.router.ServeHTTP(rw, req)

		return rw.Code
	}

	tests := []struct {
		name, path, token string
		want              int
	}{
		{"metrics need the token", "/metrics", "", http.StatusUnauthorized},
		{"metrics with the token", "/metrics", "s3cret", http.StatusOK},
		{"profiler needs the token", "/debug/pprof/cmdline", "", http.StatusUnauthorized},
		{"profiler with the token", "/debug/pprof/cmdline", "s3cret", http.StatusOK},
		{"no API", "/api/v1/repositories", "s3cret", http.StatusNotFound},
		{"no admin profiler", "/api/v1/admin/pprof/cmdline", "s3cret", http.StatusNotFound},
		{"no repository routes", "/repos/maven/x", "s3cret", http.StatusNotFound},
		{"no docker registry", "/v2/", "s3cret", http.StatusNotFound},
		{"no federation", "/federation/v1/log", "s3cret", http.StatusNotFound},
		{"no web UI", "/", "", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := get(tt.path, tt.token); got != tt.want {
				t.Errorf("GET %s = %d, want %d", tt.path, got, tt.want)
			}
		})
	}
}

// Without a worker role the profiler stays where it always was, behind the
// admin API; the operational routes do not grow a second one.
func TestWebAndAllDoNotMountTheWorkerProfiler(t *testing.T) {
	for _, role := range []string{config.RoleAll, config.RoleWeb} {
		t.Run(role, func(t *testing.T) {
			t.Setenv("METRICS_AUTH_TOKEN", "s3cret")

			a := roleApp(t, role)
			a.cfg.Server.PprofEnabled = true
			a.web.router = chi.NewRouter()
			a.registerOperationalRoutes()

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/debug/pprof/cmdline", http.NoBody)
			req.Header.Set("Authorization", "Bearer s3cret")

			rw := httptest.NewRecorder()
			a.web.router.ServeHTTP(rw, req)

			if rw.Code != http.StatusNotFound {
				t.Errorf("GET /debug/pprof/cmdline = %d, want 404", rw.Code)
			}
		})
	}
}

func TestKickPullQueuesAWorkerRequest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })

	a := roleApp(t, config.RoleWeb)

	mock.ExpectExec("INSERT INTO worker_requests").
		WithArgs(federationKickKind, "pull", nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	kickPull(workq.NewStore(db), a.log)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A lost kick is logged, never fatal: the pull scheduler polls anyway.
func TestKickPullLogsALostKick(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = db.Close() })

	a, logPath := newTestApp(t, config.Defaults())

	mock.ExpectExec("INSERT INTO worker_requests").WillReturnError(errors.New("connection refused"))

	kickPull(workq.NewStore(db), a.log)

	_ = a.log.Sync()

	logged, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logged), "queueing a pull for the worker failed") {
		t.Errorf("the failure is not logged: %s", logged)
	}
}
