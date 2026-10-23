// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

func statusByName(t *testing.T, r *Registry, name string) ScannerStatus {
	t.Helper()

	for _, s := range r.Status() {
		if s.Name == name {
			return s
		}
	}

	t.Fatalf("no status for %q", name)

	return ScannerStatus{}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestRegistryStatus(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	t.Run("osv is always ready", func(t *testing.T) {
		withoutScannerBinaries(t)

		s := statusByName(t, NewRegistry(log, Options{CacheDir: t.TempDir()}), "osv")
		if !s.Available || !s.Ready || s.Code != "" || s.Message != "" {
			t.Errorf("osv status = %+v", s)
		}
	})

	t.Run("missing binary on PATH", func(t *testing.T) {
		withoutScannerBinaries(t)

		s := statusByName(t, NewRegistry(log, Options{CacheDir: t.TempDir()}), "trivy")
		if s.Available || s.Ready || s.Code != CodeScannerUnavailable || s.DocsURL != DocsURL {
			t.Errorf("trivy status = %+v", s)
		}

		if !strings.Contains(s.Message, "OPENARTIFACT_TRIVY_PATH") {
			t.Errorf("message %q does not name the setting that fixes it", s.Message)
		}
	})

	t.Run("configured path that does not exist", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope")
		s := statusByName(t, NewRegistry(log, Options{CacheDir: t.TempDir(), Grype: GrypeOptions{Path: missing}}), "grype")

		if s.Available || s.Code != CodeScannerUnavailable {
			t.Fatalf("grype status = %+v", s)
		}

		if strings.Contains(s.Message, missing) {
			t.Errorf("message %q leaks the configured path", s.Message)
		}
	})

	t.Run("configured path wins over PATH", func(t *testing.T) {
		installFakeScanner(t, "trivy", "exit 0")

		bin := filepath.Join(t.TempDir(), "my-trivy")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}

		s := statusByName(t, NewRegistry(log, Options{CacheDir: seededCache(t), Trivy: TrivyOptions{Path: bin}}), "trivy")
		if !s.Ready || s.Path != bin {
			t.Errorf("trivy status = %+v, want ready at %s", s, bin)
		}
	})
}

func TestRegistryStatusDatabaseStates(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	installFakeScanner(t, "trivy", "exit 0")

	tests := []struct {
		name      string
		offline   bool
		seed      bool
		updating  bool
		failed    bool
		wantState string
		wantReady bool
	}{
		{name: "database present", seed: true, wantState: DBStateReady, wantReady: true},
		{name: "never downloaded", wantState: DBStateMissing},
		{name: "downloading", updating: true, wantState: DBStateUpdating},
		{name: "last download failed", failed: true, wantState: DBStateFailed},
		{name: "offline without database", offline: true, wantState: DBStateMissing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.seed {
				dir = seededCache(t)
			}

			r := NewRegistry(log, Options{CacheDir: dir, Offline: tt.offline})

			if tt.updating {
				r.trivy.beginUpdate()
			}

			if tt.failed {
				r.trivy.beginUpdate()
				r.trivy.endUpdate(errDBUpdateRun)
			}

			s := statusByName(t, r, "trivy")
			if s.DBState != tt.wantState || s.Ready != tt.wantReady || !s.Available {
				t.Fatalf("status = %+v, want state %q ready %v", s, tt.wantState, tt.wantReady)
			}

			if tt.wantReady {
				if s.Code != "" || s.DBUpdatedAt.IsZero() {
					t.Errorf("ready status must have no code and a database time: %+v", s)
				}

				return
			}

			if s.Code != CodeScannerDatabase || s.Message == "" || s.DocsURL != DocsURL {
				t.Errorf("not-ready status must carry code, message and docs link: %+v", s)
			}

			if strings.Contains(s.Message, dir) {
				t.Errorf("message %q leaks the cache directory", s.Message)
			}
		})
	}
}

// A scan must fail fast as a database failure, without starting the binary,
// while the cache holds no database.
func TestScanWithoutDatabaseFailsWithoutRunningBinary(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	marker := filepath.Join(t.TempDir(), "ran")
	installFakeScanner(t, "trivy", "touch "+marker)

	backend := NewRegistry(log, Options{CacheDir: t.TempDir()}).Backend("trivy")

	_, err := backend.Scan(context.Background(), leftPad("npm"))
	if se := asScanError(t, err); se.Kind != FailureDatabase {
		t.Fatalf("kind = %q (%v), want %q", se.Kind, err, FailureDatabase)
	}

	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the scanner binary ran although there is no database")
	}
}

// Scans must never download: they pass the cache directory and the flags that
// switch the update off, and a configured mirror only matters to the updater.
func TestScanCommandLines(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	t.Run("trivy", func(t *testing.T) {
		argsFile := filepath.Join(t.TempDir(), "args")
		installFakeScanner(t, "trivy", `printf '%s\n' "$@" > `+argsFile+`; echo '{"Results":[]}'`)

		cache := seededCache(t)

		_, err := NewRegistry(log, Options{CacheDir: cache, Trivy: TrivyOptions{DBRepository: "mirror.example/trivy-db"}}).
			Backend("trivy").Scan(context.Background(), leftPad("npm"))
		if err != nil {
			t.Fatal(err)
		}

		raw, _ := os.ReadFile(argsFile)
		args := string(raw)

		for _, want := range []string{"--skip-db-update", "--skip-java-db-update", "--offline-scan", "--cache-dir=" + filepath.Join(cache, "trivy")} {
			if !strings.Contains(args, want) {
				t.Errorf("trivy args miss %q:\n%s", want, args)
			}
		}

		if strings.Contains(args, "mirror.example") {
			t.Errorf("a scan must not carry the database mirror:\n%s", args)
		}
	})

	t.Run("grype", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "env")
		installFakeScanner(t, "grype", `echo "$GRYPE_DB_CACHE_DIR $GRYPE_DB_AUTO_UPDATE" > `+envFile+`; echo '{"matches":[]}'`)

		cache := seededCache(t)

		_, err := NewRegistry(log, Options{CacheDir: cache}).Backend("grype").Scan(context.Background(), leftPad("npm"))
		if err != nil {
			t.Fatal(err)
		}

		raw, _ := os.ReadFile(envFile)
		if got, want := strings.TrimSpace(string(raw)), filepath.Join(cache, "grype")+" false"; got != want {
			t.Errorf("grype env = %q, want %q", got, want)
		}
	})
}

func TestScanFailureNamingTheDatabaseIsClassified(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	installFakeScanner(t, "trivy", `echo "FATAL init error: DB error: failed to open db" >&2; exit 1`)

	_, err := testRegistry(t, log).Backend("trivy").Scan(context.Background(), leftPad("npm"))
	if se := asScanError(t, err); se.Kind != FailureDatabase {
		t.Fatalf("kind = %q, want %q", se.Kind, FailureDatabase)
	}
}

// The updater downloads the database of an available scanner with the
// configured mirror and cache directory, and the scanner becomes ready.
func TestStartDownloadsDatabase(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	cache := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args")

	// Fake trivy: on --download-db-only create the files a real download would.
	installFakeScanner(t, "trivy", `
printf '%s\n' "$@" >> `+argsFile+`
for a in "$@"; do
  case "$a" in
    --cache-dir=*) dir="${a#--cache-dir=}" ;;
    --download-db-only) mkdir -p "$dir/db"; echo x > "$dir/db/trivy.db"; echo '{}' > "$dir/db/metadata.json" ;;
  esac
done`)
	installFakeScanner(t, "grype", `mkdir -p "$GRYPE_DB_CACHE_DIR/6"; echo x > "$GRYPE_DB_CACHE_DIR/6/vulnerability.db"; echo "$GRYPE_DB_UPDATE_URL" > `+filepath.Join(t.TempDir(), "unused"))

	r := NewRegistry(log, Options{CacheDir: cache, Trivy: TrivyOptions{DBRepository: "mirror.example/trivy-db"}, Grype: GrypeOptions{DBURL: "https://mirror.example/grype/listing.json"}})

	if statusByName(t, r, "trivy").Ready {
		t.Fatal("trivy ready before any download")
	}

	r.Start(context.Background())
	t.Cleanup(r.Stop)

	waitFor(t, "trivy and grype databases", func() bool {
		return statusByName(t, r, "trivy").Ready && statusByName(t, r, "grype").Ready
	})

	raw, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--download-db-only", "--db-repository=mirror.example/trivy-db", "--cache-dir=" + filepath.Join(cache, "trivy")} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("trivy update args miss %q:\n%s", want, raw)
		}
	}

	if strings.Contains(string(raw), "java") {
		t.Errorf("the Java database must not be downloaded unless configured:\n%s", raw)
	}
}

func TestStartDownloadsJavaDatabaseOnlyWhenConfigured(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	argsFile := filepath.Join(t.TempDir(), "args")
	installFakeScanner(t, "trivy", `printf '%s\n' "$@" >> `+argsFile)

	r := NewRegistry(log, Options{CacheDir: t.TempDir(), Trivy: TrivyOptions{JavaDBRepository: "mirror.example/trivy-java-db"}})
	r.Start(context.Background())
	t.Cleanup(r.Stop)

	waitFor(t, "both downloads", func() bool {
		raw, _ := os.ReadFile(argsFile)
		return strings.Contains(string(raw), "--download-java-db-only")
	})

	raw, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(raw), "--java-db-repository=mirror.example/trivy-java-db") {
		t.Errorf("java update args:\n%s", raw)
	}
}

func TestStartOfflineNeverRunsTheBinary(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	marker := filepath.Join(t.TempDir(), "ran")
	installFakeScanner(t, "trivy", "touch "+marker)
	installFakeScanner(t, "grype", "touch "+marker)

	r := NewRegistry(log, Options{CacheDir: t.TempDir(), Offline: true})

	// Synchronous: a Start/Stop pair could be cancelled before the scanner
	// goroutines reach the guard under test.
	r.runTool(context.Background(), &r.trivy, r.updateTrivyDB)
	r.runTool(context.Background(), &r.grype, r.updateGrypeDB)

	if _, err := os.Stat(marker); err == nil {
		t.Error("offline mode ran a scanner binary")
	}
}

func TestStartSkipsUnavailableScanners(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	withoutScannerBinaries(t)

	r := NewRegistry(log, Options{CacheDir: t.TempDir()})
	r.Start(context.Background())
	r.Stop() // returns at once: no updater was started
}

// A failed download is reported, retried with backoff and recovers.
func TestUpdaterRetriesAfterFailure(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	cache := t.TempDir()
	allowFile := filepath.Join(t.TempDir(), "allow")

	installFakeScanner(t, "trivy", `
[ -f `+allowFile+` ] || { echo "failed to download vulnerability DB" >&2; exit 1; }
for a in "$@"; do case "$a" in --cache-dir=*) dir="${a#--cache-dir=}" ;; esac; done
mkdir -p "$dir/db"; echo x > "$dir/db/trivy.db"; echo '{}' > "$dir/db/metadata.json"`)

	r := NewRegistry(log, Options{CacheDir: cache})
	r.retryInitial, r.retryMax = 10*time.Millisecond, 20*time.Millisecond
	r.Start(context.Background())
	t.Cleanup(r.Stop)

	waitFor(t, "failed state", func() bool { return statusByName(t, r, "trivy").DBState == DBStateFailed })

	if s := statusByName(t, r, "trivy"); s.Ready || strings.Contains(s.Message, "failed to download") {
		t.Errorf("status while failing = %+v (message must not echo stderr)", s)
	}

	if err := os.WriteFile(allowFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "recovery", func() bool { return statusByName(t, r, "trivy").Ready })
}

// With an update interval the database is refreshed again and again; Stop ends it.
func TestUpdaterRefreshesEveryInterval(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")

	counter := filepath.Join(t.TempDir(), "count")
	installFakeScanner(t, "trivy", `echo run >> `+counter)

	r := NewRegistry(log, Options{CacheDir: t.TempDir(), UpdateInterval: 10 * time.Millisecond})
	r.Start(context.Background())

	waitFor(t, "three refreshes", func() bool {
		raw, _ := os.ReadFile(counter)
		return strings.Count(string(raw), "run") >= 3
	})

	// Stop waits for the updater goroutine, so the count is final afterwards.
	r.Stop()

	raw, _ := os.ReadFile(counter)
	before := strings.Count(string(raw), "run")

	r.Stop()

	raw, _ = os.ReadFile(counter)
	if after := strings.Count(string(raw), "run"); after != before {
		t.Errorf("updater kept running after Stop: %d -> %d", before, after)
	}
}
