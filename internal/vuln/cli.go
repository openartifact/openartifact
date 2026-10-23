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

	// installAuto lets the registry download the pinned release when neither
	// the configured path nor PATH provides the binary; installDir is where
	// the verified copy goes (<cacheDir base>/bin/<name>).
	installAuto bool
	installDir  string
	// installRefusal, when set, is why install "auto" was refused at
	// construction (shown instead of an install state); platform is
	// "<goos>/<goarch>", for the unsupported-platform message.
	installRefusal string
	platform       string

	mu          sync.Mutex
	updating    bool
	updateFail  bool
	lastSuccess time.Time

	// Installer state: installState is "", installing, ready or failed;
	// installCode is the failure's API code; installedPath is the verified
	// binary once it passed the exec probe.
	installState  string
	installCode   string
	installDetail string
	installedPath string
	// installedSum is the pinned sha256 of the installed binary and
	// installedInfo what stat said when it was verified: a change of file,
	// size or mtime makes lookup hash it again before it is trusted.
	installedSum  string
	installedInfo os.FileInfo
}

func newCLITool(name, path, cacheDir string, dbPresent func(string) bool) cliTool {
	command := name
	if path != "" {
		command = path
	}

	return cliTool{name: name, command: command, cacheDir: cacheDir, dbPresent: dbPresent}
}

// lookup resolves the binary by precedence: the configured path, else PATH,
// else (install "auto") the verified copy the installer put in the cache. An
// error means the scanner is unavailable.
func (t *cliTool) lookup() (string, error) {
	p, err := exec.LookPath(t.command)
	if err == nil {
		return p, nil
	}

	if t.installAuto {
		if installed := t.verifiedInstalled(); installed != "" {
			return installed, nil
		}
	}

	return "", err
}

// verifiedInstalled returns the installed binary when it is still the file
// that passed verification: its directories and the file belong to the server's
// user and nobody else can write them, and, if the file changed since (another
// inode, size or mtime), it still hashes to the pinned checksum.
func (t *cliTool) verifiedInstalled() string {
	t.mu.Lock()
	path, sum, prev := t.installedPath, t.installedSum, t.installedInfo
	t.mu.Unlock()

	if path == "" {
		return ""
	}

	info, err := checkInstallTree(filepath.Dir(t.installDir), path)
	if err != nil {
		return ""
	}

	if prev != nil && sameFileState(prev, info) {
		return path
	}

	if got, err := fileSHA256(path); err != nil || got != sum {
		return ""
	}

	t.mu.Lock()
	t.installedInfo = info
	t.mu.Unlock()

	return path
}

// preflight returns the binary a scan runs, or the failure the scan would hit
// before it can start: the binary cannot be found or the database is absent.
func (t *cliTool) preflight() (string, *ScanError) {
	bin, err := t.lookup()
	if err != nil {
		return "", newScanError(t.name, FailureUnavailable, err)
	}

	if !t.dbPresent(t.cacheDir) {
		return "", newScanError(t.name, FailureDatabase, fmt.Errorf("%w: %s", errDBMissing, t.cacheDir))
	}

	return bin, nil
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
		return t.unavailableStatus(offline, pathEnv)
	}

	t.mu.Lock()
	updating, failed, last := t.updating, t.updateFail, t.lastSuccess
	installed := t.installedPath
	t.mu.Unlock()

	st := ScannerStatus{Name: t.name, Available: true, Path: resolved, CacheDir: t.cacheDir}
	if installed != "" && resolved == installed {
		st.InstallState = InstallStateReady
	}

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
		filepath.Join(dir, grypeSchemaGlob, "vulnerability.db"),
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

// grypeSchemaGlob matches grype's schema directories ("5", "6", ...). Grype
// downloads into grype-db-download* directories that hold a vulnerability.db
// long before it is activated, so a wildcard would count a half-done update.
const grypeSchemaGlob = "[0-9]*"

// grypeDownloadGlob matches the directories grype downloads into.
const grypeDownloadGlob = "grype-db-download*"

// grypeDBPresent reports whether a grype cache directory holds an activated
// database (<cacheDir>/<schema version>/vulnerability.db).
func grypeDBPresent(cacheDir string) bool {
	matches, _ := filepath.Glob(filepath.Join(cacheDir, grypeSchemaGlob, "vulnerability.db"))

	return slices.ContainsFunc(matches, fileExists)
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)

	return err == nil && fi.Mode().IsRegular()
}

// updateTrivyDB downloads (or refreshes) the trivy database, and the Java
// database when a repository for it is configured.
//
// Trivy deletes the database directory it is about to replace, so refreshing
// the live one would leave scans without a database for as long as the
// download takes. The download therefore runs in a staging directory seeded
// with hard links to the live files (Trivy still sees the current metadata and
// skips an unneeded download) and only a finished, different database is
// swapped in, atomically where the filesystem allows.
func (r *Registry) updateTrivyDB(ctx context.Context) error {
	t := &r.trivy

	return t.withLock(ctx, func() error {
		stage := filepath.Join(t.cacheDir, trivyStagingDir)

		if err := os.RemoveAll(stage); err != nil {
			return fmt.Errorf("clear %s staging dir: %w", t.name, err)
		}

		if err := os.MkdirAll(stage, 0o750); err != nil {
			return fmt.Errorf("create %s staging dir: %w", t.name, err)
		}

		defer func() { _ = os.RemoveAll(stage) }()

		for _, d := range trivyDBDirs {
			seedStage(filepath.Join(t.cacheDir, d), filepath.Join(stage, d))
		}

		base := []string{"image", "--quiet", "--skip-version-check", "--cache-dir=" + stage}

		args := append(append([]string{}, base...), "--download-db-only")
		if r.opts.Trivy.DBRepository != "" {
			args = append(args, "--db-repository="+r.opts.Trivy.DBRepository)
		}

		if err := r.runUpdate(ctx, t, nil, args); err != nil {
			return err
		}

		if r.opts.Trivy.JavaDBRepository != "" {
			args = append(append([]string{}, base...), "--download-java-db-only", "--java-db-repository="+r.opts.Trivy.JavaDBRepository)

			if err := r.runUpdate(ctx, t, nil, args); err != nil {
				return err
			}
		}

		for _, d := range trivyDBDirs {
			if err := promoteStaged(filepath.Join(stage, d), filepath.Join(t.cacheDir, d)); err != nil {
				return fmt.Errorf("%w: %s: %w", errDBUpdateRun, t.name, err)
			}
		}

		return nil
	})
}

// trivyStagingDir is where a Trivy refresh downloads, inside the cache
// directory so that a rename into place never crosses filesystems.
const trivyStagingDir = ".staging"

// trivyDBDirs are the directories of Trivy's cache directory a database
// download writes: the vulnerability database and the optional Java index.
//
//nolint:gochecknoglobals // read-only table
var trivyDBDirs = []string{"db", "java-db"}

// seedStage hard-links the files of the live database directory into staged.
// Trivy replaces the files by removing them and writing new ones, which leaves
// the live inodes alone. If linking is not possible the stage stays empty and
// Trivy downloads from scratch.
func seedStage(live, staged string) {
	entries, err := os.ReadDir(live)
	if err != nil {
		return
	}

	if err := os.MkdirAll(staged, 0o750); err != nil {
		return
	}

	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}

		if err := os.Link(filepath.Join(live, e.Name()), filepath.Join(staged, e.Name())); err != nil {
			_ = os.RemoveAll(staged)

			return
		}
	}
}

// promoteStaged makes the staged directory the live one unless Trivy left it
// as it was (every file is still the live file).
func promoteStaged(staged, live string) error {
	if _, err := os.Stat(staged); err != nil {
		return nil //nolint:nilerr // nothing was downloaded into this directory
	}

	if sameFiles(staged, live) {
		return nil
	}

	return swapDirs(staged, live)
}

// sameFiles reports whether a and b hold the same regular files (the same
// inodes, not copies).
func sameFiles(a, b string) bool {
	entries, err := os.ReadDir(a)
	if err != nil {
		return false
	}

	other, err := os.ReadDir(b)
	if err != nil || len(other) != len(entries) {
		return false
	}

	for _, e := range entries {
		fa, errA := os.Stat(filepath.Join(a, e.Name()))
		fb, errB := os.Stat(filepath.Join(b, e.Name()))

		if errA != nil || errB != nil || !os.SameFile(fa, fb) {
			return false
		}
	}

	return true
}

// updateGrypeDB downloads (or refreshes) the grype database. Grype downloads
// into a grype-db-download* directory and activates it by renaming; a killed
// run leaves that directory behind (up to the size of the database), so the
// leftovers are removed before and after the run.
func (r *Registry) updateGrypeDB(ctx context.Context) error {
	t := &r.grype

	env := t.env()
	if r.opts.Grype.DBURL != "" {
		env = append(env, "GRYPE_DB_UPDATE_URL="+r.opts.Grype.DBURL)
	}

	// An update must be allowed to download whatever scans are told not to.
	env = append(env, "GRYPE_DB_AUTO_UPDATE=true")

	return t.withLock(ctx, func() error {
		if err := os.MkdirAll(t.cacheDir, 0o750); err != nil {
			return fmt.Errorf("create %s cache dir: %w", t.name, err)
		}

		removeGrypeDownloads(t.cacheDir)
		defer removeGrypeDownloads(t.cacheDir)

		return r.runUpdate(ctx, t, env, []string{"db", "update", "--quiet"})
	})
}

// removeGrypeDownloads deletes grype's unfinished download directories.
func removeGrypeDownloads(cacheDir string) {
	matches, _ := filepath.Glob(filepath.Join(cacheDir, grypeDownloadGlob))
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
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

	bin, err := t.lookup()
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errDBUpdateRun, t.name, err)
	}

	_, stderr, err := runScanner(ctx, env, bin, args...)
	if err != nil {
		return fmt.Errorf("%w: %s: %w (stderr: %s)", errDBUpdateRun, t.name, err, stderr)
	}

	return nil
}
