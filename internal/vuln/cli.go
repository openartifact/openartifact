// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

var (
	errDBMissing   = errors.New("vulnerability database not present in the cache directory")
	errDBUpdateRun = errors.New("scanner database update failed")
)

// dbUpdateTimeout bounds one database download.
const dbUpdateTimeout = 15 * time.Minute

// cliTool is one external scanner binary with its database directory and the
// state of its last database update.
type cliTool struct {
	name     string
	command  string
	cacheDir string
	// dbPresent reports whether cacheDir holds a database.
	dbPresent func(cacheDir string) bool

	mu          sync.Mutex
	updating    bool
	updateFail  bool
	lastSuccess time.Time
}

func newCLITool(name, path, cacheDir string, dbPresent func(string) bool) cliTool {
	command := name
	if path != "" {
		command = path
	}

	return cliTool{name: name, command: command, cacheDir: cacheDir, dbPresent: dbPresent}
}

// lookup resolves the binary; an error means the scanner is unavailable.
func (t *cliTool) lookup() (string, error) {
	return exec.LookPath(t.command)
}

// preflight returns the failure a scan would hit before it can start: the
// binary cannot be found or the database is absent.
func (t *cliTool) preflight() *ScanError {
	if _, err := t.lookup(); err != nil {
		return newScanError(t.name, FailureUnavailable, err)
	}

	if !t.dbPresent(t.cacheDir) {
		return newScanError(t.name, FailureDatabase, fmt.Errorf("%w: %s", errDBMissing, t.cacheDir))
	}

	return nil
}

func (t *cliTool) beginUpdate() {
	t.mu.Lock()
	t.updating = true
	t.mu.Unlock()
}

func (t *cliTool) endUpdate(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.updating = false
	t.updateFail = err != nil

	if err == nil {
		t.lastSuccess = time.Now()
	}
}

func (t *cliTool) status(offline bool, pathEnv string) ScannerStatus {
	resolved, err := t.lookup()
	if err != nil {
		msg := t.name + " was not found on the server's PATH: install it or set " + pathEnv + " (scanners." + t.name + ".path)."
		if t.command != t.name {
			msg = "The configured " + t.name + " binary (" + pathEnv + ") is missing or not executable."
		}

		return ScannerStatus{Name: t.name, Code: CodeScannerUnavailable, Message: msg, DocsURL: DocsURL}
	}

	t.mu.Lock()
	updating, failed, last := t.updating, t.updateFail, t.lastSuccess
	t.mu.Unlock()

	st := ScannerStatus{Name: t.name, Available: true, Path: resolved, CacheDir: t.cacheDir}

	if t.dbPresent(t.cacheDir) {
		st.Ready = true
		st.DBState = DBStateReady
		st.DBUpdatedAt = last

		if st.DBUpdatedAt.IsZero() {
			st.DBUpdatedAt = dbMTime(t.cacheDir)
		}

		return st
	}

	st.Code = CodeScannerDatabase
	st.DocsURL = DocsURL

	switch {
	case updating:
		st.DBState = DBStateUpdating
		st.Message = "The vulnerability database is being downloaded: retry in a few minutes."
	case failed:
		st.DBState = DBStateFailed
		st.Message = "The vulnerability database could not be downloaded: check outbound access to the database host (or the configured mirror) and that the cache directory is writable. The server log has the cause."
	case offline:
		st.DBState = DBStateMissing
		st.Message = "Offline mode is on and the cache directory holds no vulnerability database: copy one in."
	default:
		st.DBState = DBStateMissing
		st.Message = "No vulnerability database has been downloaded yet."
	}

	return st
}

// dbMTime returns the modification time of the newest database file under dir.
func dbMTime(dir string) time.Time {
	var newest time.Time

	for _, pattern := range []string{
		filepath.Join(dir, "db", "metadata.json"),
		filepath.Join(dir, "*", "vulnerability.db"),
	} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.ModTime().After(newest) {
				newest = fi.ModTime()
			}
		}
	}

	return newest
}

// trivyDBPresent reports whether a trivy cache directory holds the database.
func trivyDBPresent(cacheDir string) bool {
	return fileExists(filepath.Join(cacheDir, "db", "trivy.db")) &&
		fileExists(filepath.Join(cacheDir, "db", "metadata.json"))
}

// grypeDBPresent reports whether a grype cache directory holds a database
// (<cacheDir>/<schema version>/vulnerability.db).
func grypeDBPresent(cacheDir string) bool {
	matches, _ := filepath.Glob(filepath.Join(cacheDir, "*", "vulnerability.db"))

	return slices.ContainsFunc(matches, fileExists)
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)

	return err == nil && fi.Mode().IsRegular()
}

// updateTrivyDB downloads (or refreshes) the trivy database, and the Java
// database when a repository for it is configured.
func (r *Registry) updateTrivyDB(ctx context.Context) error {
	base := []string{"image", "--quiet", "--skip-version-check", "--cache-dir=" + r.trivy.cacheDir}

	args := append(append([]string{}, base...), "--download-db-only")
	if r.opts.Trivy.DBRepository != "" {
		args = append(args, "--db-repository="+r.opts.Trivy.DBRepository)
	}

	if err := r.runUpdate(ctx, &r.trivy, nil, args); err != nil {
		return err
	}

	if r.opts.Trivy.JavaDBRepository == "" {
		return nil
	}

	args = append(append([]string{}, base...), "--download-java-db-only", "--java-db-repository="+r.opts.Trivy.JavaDBRepository)

	return r.runUpdate(ctx, &r.trivy, nil, args)
}

// updateGrypeDB downloads (or refreshes) the grype database.
func (r *Registry) updateGrypeDB(ctx context.Context) error {
	env := r.grype.env()
	if r.opts.Grype.DBURL != "" {
		env = append(env, "GRYPE_DB_UPDATE_URL="+r.opts.Grype.DBURL)
	}

	// An update must be allowed to download whatever scans are told not to.
	env = append(env, "GRYPE_DB_AUTO_UPDATE=true")

	return r.runUpdate(ctx, &r.grype, env, []string{"db", "update", "--quiet"})
}

// env is the environment that points grype at its cache directory and stops it
// from checking for application updates; scans add GRYPE_DB_AUTO_UPDATE=false.
func (t *cliTool) env() []string {
	return []string{"GRYPE_DB_CACHE_DIR=" + t.cacheDir, "GRYPE_CHECK_FOR_APP_UPDATE=false"}
}

func (r *Registry) runUpdate(ctx context.Context, t *cliTool, env, args []string) error {
	if err := os.MkdirAll(t.cacheDir, 0o750); err != nil {
		return fmt.Errorf("create %s cache dir: %w", t.name, err)
	}

	_, stderr, err := runScanner(ctx, env, t.command, args...)
	if err != nil {
		return fmt.Errorf("%w: %s: %w (stderr: %s)", errDBUpdateRun, t.name, err, stderr)
	}

	return nil
}
