// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/netutil"
)

const (
	// installTimeout bounds one download-extract-probe attempt.
	installTimeout = 10 * time.Minute
	// probeTimeout bounds the "--version" run that proves the binary executes.
	probeTimeout = 30 * time.Second
	// defaultMaxArchive and defaultMaxBinary cap what an install may write: the
	// pinned releases are about 70 MB archives holding a 180 MB binary.
	defaultMaxArchive = 256 << 20
	defaultMaxBinary  = 512 << 20
)

// Install result labels (bounded: they are metric label values).
const (
	resultInstalled   = "installed"
	resultReused      = "reused"
	resultUnsupported = "unsupported_platform"
	resultDownload    = "download_failed"
	resultChecksum    = "checksum_mismatch"
	resultExtract     = "extract_failed"
	resultNoexec      = "noexec"
	resultInsecure    = "insecure_location"
	resultFailed      = "failed"
)

var (
	errNotInstalled   = errors.New("no verified installed copy and downloads are disabled")
	errDownload       = errors.New("download failed")
	errDownloadStatus = errors.New("unexpected HTTP status")
	errArchiveSize    = errors.New("archive exceeds the size limit")
	errChecksum       = errors.New("archive checksum does not match the pinned value")
	errBinaryChecksum = errors.New("binary checksum does not match the pinned value")
	errExtract        = errors.New("extract failed")
	errUnsafeEntry    = errors.New("archive entry has an unsafe path")
	errLinkEntry      = errors.New("archive contains a link")
	errNotRegular     = errors.New("archive entry is not a regular file")
	errBinarySize     = errors.New("binary exceeds the size limit")
	errBinaryMissing  = errors.New("archive does not contain the scanner binary")
	errNoexec         = errors.New("installed binary cannot be executed (is the cache directory mounted noexec?)")
)

// probeArgs are the arguments that make each scanner print its version and exit.
//
//nolint:gochecknoglobals // read-only table of CLI version flags
var probeArgs = map[string][]string{
	scannerTrivy: {"--version"},
	scannerGrype: {"version"},
}

// probeBinary runs bin with args and reports whether it started and exited 0.
func probeBinary(ctx context.Context, bin string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	_, _, err := runScanner(ctx, nil, bin, args...)

	return err
}

// classifyInstall maps an install error to its metric result and API code;
// terminal errors would fail the same way on every retry. It returns the
// result, the code and whether the error is terminal, in that order.
func classifyInstall(err error) (string, string, bool) {
	switch {
	case errors.Is(err, errNoBuild):
		return resultUnsupported, CodeScannerInstallFailed, true
	case errors.Is(err, errNoexec):
		return resultNoexec, CodeScannerInstallNoexec, true
	case errors.Is(err, errInsecureInstall):
		return resultInsecure, CodeScannerInstallFailed, true
	case errors.Is(err, errChecksum), errors.Is(err, errBinaryChecksum):
		return resultChecksum, CodeScannerInstallFailed, false
	case errors.Is(err, errDownload):
		return resultDownload, CodeScannerInstallFailed, false
	case errors.Is(err, errExtract):
		return resultExtract, CodeScannerInstallFailed, false
	default:
		return resultFailed, CodeScannerInstallFailed, false
	}
}

func (t *cliTool) beginInstall() {
	t.mu.Lock()
	t.installState = InstallStateInstalling
	t.installCode = ""
	t.installDetail = ""
	t.mu.Unlock()
}

// installedBinary is a binary that passed verification: where it is, the
// pinned sha256 it matched and its stat at that moment.
type installedBinary struct {
	path string
	sum  string
	info os.FileInfo
}

// endInstallOK records a verified installed binary.
func (t *cliTool) endInstallOK(b installedBinary) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.installState = InstallStateReady
	t.installedPath, t.installedSum, t.installedInfo = b.path, b.sum, b.info
}

// endInstallFailed records a failed install: its API code and the result
// label, which selects the message.
func (t *cliTool) endInstallFailed(code, result string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.installState = InstallStateFailed
	t.installCode = code
	t.installDetail = result
}

// unavailableStatus describes a scanner whose binary cannot be resolved.
func (t *cliTool) unavailableStatus(offline bool, pathEnv string) ScannerStatus {
	st := ScannerStatus{Name: t.name, Code: CodeScannerUnavailable, DocsURL: DocsURL}
	installEnv := strings.TrimSuffix(pathEnv, "_PATH") + "_INSTALL"

	if t.command != t.name {
		st.Message = "The configured " + t.name + " binary (" + pathEnv + ") is missing or not executable."

		return st
	}

	notFound := t.name + " was not found on the server's PATH: install it or set " + pathEnv + " (scanners." + t.name + ".path)."

	if t.installRefusal != "" {
		st.Code, st.InstallState = CodeScannerInstallFailed, InstallStateFailed
		st.Message = t.installRefusal

		return st
	}

	if !t.installAuto {
		st.Message = notFound + " Or set " + installEnv + "=auto (scanners." + t.name + ".install) to download the pinned release."

		return st
	}

	t.mu.Lock()
	state, code, detail, hadBinary := t.installState, t.installCode, t.installDetail, t.installedPath != ""
	t.mu.Unlock()

	switch {
	case state == InstallStateFailed:
		st.Code, st.InstallState = code, InstallStateFailed
		st.Message = t.installFailureMessage(code, detail)
	case hadBinary:
		// The installer finished, but the file is gone or no longer what was
		// verified: nothing is installing, and nothing will until a restart.
		st.Code, st.InstallState = CodeScannerInstallFailed, InstallStateFailed
		st.Message = "The installed " + t.name + " binary is missing or no longer matches the verified file: restart the server to install it again."
	case offline:
		st.Message = notFound + " Offline mode is on, so the pinned release is not downloaded."
	default:
		st.Code, st.InstallState = CodeScannerInstalling, InstallStateInstalling
		st.Message = "The pinned " + t.name + " release is being downloaded: retry in a minute or two."
	}

	return st
}

func (t *cliTool) installFailureMessage(code, result string) string {
	switch {
	case code == CodeScannerInstallNoexec:
		return "The downloaded " + t.name + " binary cannot be executed: the scanner cache directory is probably on a noexec mount. Mount it with exec, or install " + t.name + " yourself."
	case result == resultUnsupported:
		return "No " + t.name + " build is pinned for this platform (" + t.platform + "): install " + t.name + " yourself and put it on PATH or set its path."
	case result == resultInsecure:
		return "The " + t.name + " install directory is not private to the server's user (it must be owned by it and not writable by others): fix its ownership and permissions, or set the " + t.name + " path instead."
	default:
		return "The " + t.name + " release could not be installed: check outbound access to the download host (or scanners.downloadBaseUrl) and that the cache directory is writable. The server log has the cause."
	}
}

// ensureInstalled makes the pinned release available for t when it is not
// resolvable yet, retrying with the database updater's backoff. With
// allowDownload false it only adopts an already installed, verified copy. It
// returns once the scanner resolves, the context ends or the failure is
// terminal.
func (r *Registry) ensureInstalled(ctx context.Context, t *cliTool, allowDownload bool) {
	if _, err := t.lookup(); err == nil {
		return
	}

	retry := r.retryInitial

	for {
		t.beginInstall()

		var (
			installed installedBinary
			result    string
		)

		// One install or update of this scanner at a time, across replicas
		// sharing the cache directory too; the time limit covers the install,
		// not the wait for the lock.
		err := t.withLock(ctx, func() error {
			ictx, cancel := context.WithTimeout(ctx, installTimeout)
			defer cancel()

			var ierr error

			installed, result, ierr = r.installTool(ictx, t, allowDownload)

			return ierr
		})
		if err != nil && result == "" {
			result = resultOf(err)
		}

		if errors.Is(err, errNotInstalled) {
			t.mu.Lock()
			t.installState = ""
			t.mu.Unlock()

			return
		}

		// An attempt cut short by shutdown says nothing about the mirror.
		if ctx.Err() == nil {
			r.countInstall(t.name, result)
		}

		if err == nil {
			t.endInstallOK(installed)
			r.log.Infow("scanner installed", "scanner", t.name, "binary", installed.path, "result", result)

			return
		}

		result, code, terminal := classifyInstall(err)
		t.endInstallFailed(code, result)

		if ctx.Err() != nil {
			return
		}

		r.log.Errorw("scanner install failed",
			"scanner", t.name,
			"code", code,
			"retry_in", retry.String(),
			"error", err,
		)

		if terminal {
			return
		}

		if !sleepCtx(ctx, retry) {
			return
		}

		retry = min(retry*2, r.retryMax)
	}
}

// sleepCtx waits d and reports false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *Registry) countInstall(scanner, result string) {
	if r.metrics != nil {
		r.metrics.ScannerInstallAttemptsTotal.WithLabelValues(scanner, result).Inc()
	}
}

// installTool puts the pinned release of t into t.installDir/<version>/ and
// returns it with the result label. A copy that is already there is reused
// only when its checksum matches the manifest; a binary is executed (the
// probe) only after its sha256 matched the pinned value and its location was
// found private to the server's user.
func (r *Registry) installTool(ctx context.Context, t *cliTool, allowDownload bool) (installedBinary, string, error) {
	m, err := r.manifest()
	if err != nil {
		return installedBinary{}, resultFailed, err
	}

	scanner, build, err := m.build(t.name, r.goos, r.goarch)
	if err != nil {
		return installedBinary{}, resultUnsupported, err
	}

	dir := filepath.Join(t.installDir, scanner.Version)
	bin := filepath.Join(dir, scanner.Binary)
	ib := installedBinary{path: bin, sum: build.BinarySHA256}

	if sum, err := fileSHA256(bin); err == nil {
		if sum == build.BinarySHA256 {
			return r.probeInstalled(ctx, t, ib, resultReused)
		}

		// Truncated or tampered: never execute it.
		r.log.Warnw("installed scanner failed its checksum; discarding it", "scanner", t.name)

		if err := os.Remove(bin); err != nil {
			return installedBinary{}, resultFailed, fmt.Errorf("remove corrupt binary: %w", err)
		}
	}

	if !allowDownload {
		return installedBinary{}, "", errNotInstalled
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return installedBinary{}, resultFailed, fmt.Errorf("create install dir: %w", err)
	}

	removeStale(dir, installTimeout)

	archive, err := r.download(ctx, r.archiveURL(build), dir, build.ArchiveSHA256)
	if err != nil {
		return installedBinary{}, resultOf(err), err
	}

	defer os.Remove(archive)

	if err := extractBinary(archive, scanner.Binary, bin, build.BinarySHA256, r.maxBinaryBytes()); err != nil {
		return installedBinary{}, resultOf(err), err
	}

	return r.probeInstalled(ctx, t, ib, resultInstalled)
}

func resultOf(err error) string {
	result, _, _ := classifyInstall(err)

	return result
}

// probeInstalled checks where the verified binary lives, then runs it once to
// prove it executes.
func (r *Registry) probeInstalled(ctx context.Context, t *cliTool, ib installedBinary, result string) (installedBinary, string, error) {
	info, err := checkInstallTree(filepath.Dir(t.installDir), ib.path)
	if err != nil {
		if errors.Is(err, errInsecureInstall) {
			return installedBinary{}, resultInsecure, err
		}

		return installedBinary{}, resultFailed, fmt.Errorf("check installed %s: %w", t.name, err)
	}

	ib.info = info

	if err := r.probe(ctx, ib.path, probeArgs[t.name]...); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return installedBinary{}, resultNoexec, fmt.Errorf("%w: %w", errNoexec, err)
		}

		return installedBinary{}, resultFailed, fmt.Errorf("installed %s does not run: %w", t.name, err)
	}

	return ib, result, nil
}

func (r *Registry) archiveURL(b manifestBuild) string {
	return strings.TrimRight(r.opts.DownloadBaseURL, "/") + "/" + b.Path
}

func (r *Registry) maxArchiveBytes() int64 {
	if r.maxArchive > 0 {
		return r.maxArchive
	}

	return defaultMaxArchive
}

func (r *Registry) maxBinaryBytes() int64 {
	if r.maxBinary > 0 {
		return r.maxBinary
	}

	return defaultMaxBinary
}

func (r *Registry) client(rawURL string) (*http.Client, error) {
	if r.httpClient != nil {
		return r.httpClient, nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errDownload, err)
	}

	return netutil.NewDownloadClient(installTimeout, u.Host), nil
}

// download streams rawURL into a temp file under dir while hashing it, and
// returns the file's path only if it fits the size limit and hashes to want.
func (r *Registry) download(ctx context.Context, rawURL, dir, want string) (string, error) {
	client, err := r.client(rawURL)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errDownload, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errDownload, err)
	}

	defer resp.Body.Close()

	limit := r.maxArchiveBytes()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %w: %d from %s", errDownload, errDownloadStatus, resp.StatusCode, req.URL.Host)
	}

	if resp.ContentLength > limit {
		return "", fmt.Errorf("%w: %w (%d bytes)", errDownload, errArchiveSize, resp.ContentLength)
	}

	f, err := os.CreateTemp(dir, ".archive-*")
	if err != nil {
		return "", fmt.Errorf("%w: %w", errDownload, err)
	}

	h := sha256.New()

	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}

	switch {
	case err != nil:
		err = fmt.Errorf("%w: %w", errDownload, err)
	case n > limit:
		err = fmt.Errorf("%w: %w", errDownload, errArchiveSize)
	case hex.EncodeToString(h.Sum(nil)) != strings.ToLower(want):
		err = errChecksum
	}

	if err != nil {
		_ = os.Remove(f.Name())

		return "", err
	}

	return f.Name(), nil
}

// extractBinary reads the gzip-compressed tar at archive and writes the entry
// named name to dest (0755, fsynced, renamed into place) once its sha256
// equals want. Every other entry is skipped, never extracted; an archive with
// a link or a path that escapes the archive root is rejected whole.
func extractBinary(archive, name, dest, want string, limit int64) error {
	tmp, err := extractToTemp(archive, name, filepath.Dir(dest), want, limit)
	if err != nil {
		return fmt.Errorf("%w: %w", errExtract, err)
	}

	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("%w: %w", errExtract, err)
	}

	syncDir(filepath.Dir(dest))

	return nil
}

func extractToTemp(archive, name, dir, want string, limit int64) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}

	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}

	defer gz.Close()

	tr := tar.NewReader(gz)
	tmp := ""

	fail := func(err error) (string, error) {
		if tmp != "" {
			_ = os.Remove(tmp)
		}

		return "", err
	}

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fail(err)
		}

		if unsafeEntry(hdr.Name) {
			return fail(fmt.Errorf("%w: %q", errUnsafeEntry, hdr.Name))
		}

		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			return fail(fmt.Errorf("%w: %q", errLinkEntry, hdr.Name))
		}

		if path.Clean(hdr.Name) != name || tmp != "" {
			continue
		}

		if hdr.Typeflag != tar.TypeReg {
			return fail(fmt.Errorf("%w: %q", errNotRegular, hdr.Name))
		}

		if hdr.Size > limit {
			return fail(errBinarySize)
		}

		if tmp, err = writeBinary(tr, dir, want, limit); err != nil {
			return fail(err)
		}
	}

	if tmp == "" {
		return "", errBinaryMissing
	}

	return tmp, nil
}

// writeBinary copies r (at most limit bytes) to a new executable temp file in
// dir and returns its path when the content hashes to want. The file is
// removed on any error.
func writeBinary(r io.Reader, dir, want string, limit int64) (string, error) {
	out, err := os.CreateTemp(dir, ".bin-*")
	if err != nil {
		return "", err
	}

	ok := false

	defer func() {
		if !ok {
			_ = os.Remove(out.Name())
		}
	}()

	h := sha256.New()

	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(r, limit+1))
	if err != nil {
		_ = out.Close()

		return "", err
	}

	if n > limit {
		_ = out.Close()

		return "", errBinarySize
	}

	// The file stays non-executable (0600) until its content is proven.
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(want) {
		_ = out.Close()

		return "", errBinaryChecksum
	}

	if err := out.Chmod(0o755); err != nil {
		_ = out.Close()

		return "", err
	}

	if err := out.Sync(); err != nil {
		_ = out.Close()

		return "", err
	}

	if err := out.Close(); err != nil {
		return "", err
	}

	ok = true

	return out.Name(), nil
}

// unsafeEntry reports a tar entry name that is absolute or climbs out of the
// archive root.
func unsafeEntry(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) {
		return true
	}

	for part := range strings.SplitSeq(name, "/") {
		if part == ".." {
			return true
		}
	}

	return false
}

// fileSHA256 returns the hex sha256 of the regular file at p.
func fileSHA256(p string) (string, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}

	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s", errNotRegular, filepath.Base(p))
	}

	f, err := os.Open(p)
	if err != nil {
		return "", err
	}

	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// removeStale deletes partial files an interrupted install left in dir. Files
// modified within the last olderThan are kept: they may belong to an install
// still running in another process that shares the cache directory.
func removeStale(dir string, olderThan time.Duration) {
	for _, pattern := range []string{".archive-*", ".bin-*"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, m := range matches {
			if fi, err := os.Lstat(m); err == nil && time.Since(fi.ModTime()) >= olderThan {
				_ = os.Remove(m)
			}
		}
	}
}

// syncDir flushes a directory entry (best effort: not every filesystem allows it).
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}

	_ = d.Sync()
	_ = d.Close()
}
