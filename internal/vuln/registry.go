// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/pkg/logger"
)

// DocsURL is where the scanner installation guide lives; failure messages and
// the scanner list point operators there.
const DocsURL = "https://github.com/openartifact/openartifact/blob/main/docs/security/scanning.md#installing-trivy-and-grype"

// Codes that identify an unusable scanner to API clients.
const (
	// CodeScannerUnavailable: the scanner binary is not installed or not executable.
	CodeScannerUnavailable = "scanner_unavailable"
	// CodeScannerDatabase: the binary is there but its vulnerability database is
	// missing, still downloading, or could not be downloaded.
	CodeScannerDatabase = "scanner_db_unavailable"
	// CodeScannerInstalling: the pinned release is being downloaded into the cache.
	CodeScannerInstalling = "scanner_installing"
	// CodeScannerInstallFailed: the download, checksum check or extraction failed.
	CodeScannerInstallFailed = "scanner_install_failed"
	// CodeScannerInstallNoexec: the binary was installed but cannot be executed,
	// typically because the cache directory is on a noexec mount.
	CodeScannerInstallNoexec = "scanner_install_noexec"
)

// Install states reported by Status for a scanner whose install mode is "auto".
const (
	InstallStateInstalling = "installing"
	InstallStateReady      = "ready"
	InstallStateFailed     = "failed"
)

const (
	scannerOSV   = "osv"
	scannerTrivy = "trivy"
	scannerGrype = "grype"
)

// Options configures the external scanners and their vulnerability databases.
// The zero value runs trivy and grype from PATH, keeps their databases under a
// per-user cache directory and never refreshes them (call Start to refresh).
type Options struct {
	// CacheDir is the base directory for scanner databases: trivy uses
	// <CacheDir>/trivy and grype <CacheDir>/grype. It must be writable unless
	// Offline is set. Empty means a directory under the user cache dir.
	CacheDir string
	// Offline stops the server from downloading databases. Scans then use
	// whatever the cache directory holds and fail while it holds none.
	Offline bool
	// UpdateInterval is the pause between database refreshes after a success.
	// Zero downloads only when Start runs and then never again.
	UpdateInterval time.Duration
	// DownloadBaseURL is the https origin the pinned release archives are
	// downloaded from (installs with Install set). Empty is https://github.com.
	DownloadBaseURL string
	Trivy           TrivyOptions
	Grype           GrypeOptions
}

// TrivyOptions configures the trivy binary.
type TrivyOptions struct {
	// Path is the binary; empty looks "trivy" up on PATH.
	Path string
	// Install downloads the pinned release into <CacheDir>/bin when Path is
	// empty and trivy is not on PATH. A configured Path always wins.
	Install bool
	// DBRepository is the OCI repository the vulnerability database is pulled
	// from (a mirror of ghcr.io/aquasecurity/trivy-db). Empty is the upstream default.
	DBRepository string
	// JavaDBRepository is the OCI repository of the Java index database. Empty
	// does not download it: OpenArtifact scans manifests (pom.xml, ...), never
	// JAR files, so only a custom use needs it.
	JavaDBRepository string
}

// GrypeOptions configures the grype binary.
type GrypeOptions struct {
	// Path is the binary; empty looks "grype" up on PATH.
	Path string
	// Install downloads the pinned release into <CacheDir>/bin when Path is
	// empty and grype is not on PATH. A configured Path always wins.
	Install bool
	// DBURL is the database listing URL (GRYPE_DB_UPDATE_URL) of a mirror.
	// Empty is the upstream default.
	DBURL string
}

// Registry builds scanner backends, keeps the CLI scanners' databases fresh and
// reports which scanners can run.
type Registry struct {
	log  *logger.Logger
	opts Options

	trivy cliTool
	grype cliTool

	// retryInitial and retryMax bound the pause after a failed database update.
	retryInitial time.Duration
	retryMax     time.Duration

	// Installer collaborators; tests replace them.
	manifest   func() (manifest, error)
	httpClient *http.Client
	probe      func(ctx context.Context, bin string, args ...string) error
	goos       string
	goarch     string
	// maxArchive and maxBinary cap an install's download and extracted binary;
	// zero means the defaults.
	maxArchive int64
	maxBinary  int64
	metrics    *metrics.Registry

	mu     sync.Mutex
	cancel func()
	wg     sync.WaitGroup
}

// NewRegistry returns a registry for the given options. It does not touch the
// disk or start anything.
func NewRegistry(log *logger.Logger, opts Options) *Registry {
	tempCache := false

	if opts.CacheDir == "" {
		opts.CacheDir, tempCache = defaultCacheDir()
	}

	if opts.DownloadBaseURL == "" {
		opts.DownloadBaseURL = "https://github.com"
	}

	r := &Registry{
		log:          log,
		opts:         opts,
		trivy:        newCLITool(scannerTrivy, opts.Trivy.Path, filepath.Join(opts.CacheDir, scannerTrivy), trivyDBPresent),
		grype:        newCLITool(scannerGrype, opts.Grype.Path, filepath.Join(opts.CacheDir, scannerGrype), grypeDBPresent),
		retryInitial: time.Minute,
		retryMax:     time.Hour,
		manifest:     embeddedManifest,
		probe:        probeBinary,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
	}

	// A configured path always wins over PATH and over an install.
	r.trivy.installAuto = opts.Trivy.Install && opts.Trivy.Path == ""
	r.trivy.installDir = filepath.Join(opts.CacheDir, "bin", scannerTrivy)
	r.grype.installAuto = opts.Grype.Install && opts.Grype.Path == ""
	r.grype.installDir = filepath.Join(opts.CacheDir, "bin", scannerGrype)

	for _, t := range []*cliTool{&r.trivy, &r.grype} {
		t.platform = r.goos + "/" + r.goarch

		// A downloaded binary is executed, so it never lands in a directory
		// that is shared by everyone on the host.
		if t.installAuto && tempCache {
			t.installAuto = false
			t.installRefusal = "Runtime install of " + t.name + " is disabled: no persistent, private scanner cache directory is configured. Set OPENARTIFACT_SCANNER_CACHE_DIR."

			log.Errorw("scanner install refused: the cache directory would be in the shared temp directory",
				"scanner", t.name,
				"hint", "set OPENARTIFACT_SCANNER_CACHE_DIR to a directory only the server's user can write",
			)
		}
	}

	return r
}

// WithMetrics counts install attempts in reg.ScannerInstallAttemptsTotal.
func (r *Registry) WithMetrics(reg *metrics.Registry) *Registry {
	r.metrics = reg

	return r
}

// defaultCacheDir is the scanner cache base when none is configured, and
// whether it fell back to the shared temp directory (no user cache directory).
func defaultCacheDir() (string, bool) {
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "openartifact", "scanners"), false
	}

	return filepath.Join(os.TempDir(), "openartifact-scanners"), true
}

// Backend returns the scanner backend for name; an unknown name yields OSV.
func (r *Registry) Backend(name string) ScannerBackend {
	switch strings.ToLower(name) {
	case scannerTrivy:
		return &TrivyScanner{log: r.log, tool: &r.trivy}
	case scannerGrype:
		return &GrypeScanner{log: r.log, tool: &r.grype}
	default:
		return NewOSVScanner(r.log)
	}
}

// DB states reported by Status for a CLI scanner.
const (
	DBStateReady    = "ready"
	DBStateUpdating = "updating"
	DBStateFailed   = "failed"
	DBStateMissing  = "missing"
)

// ScannerStatus says whether a scanner can run on this server.
type ScannerStatus struct {
	// Name is osv, trivy or grype.
	Name string
	// Available is false when the scanner binary cannot be started.
	Available bool
	// Ready is true when a scan can run now: OSV always, a CLI scanner when its
	// binary resolves and its vulnerability database is present.
	Ready bool
	// Path is the resolved binary and CacheDir the database directory (CLI
	// scanners only). They are for the server log and must not be sent to API clients.
	Path     string
	CacheDir string
	// DBState is one of the DBState* values for an available CLI scanner, else "".
	DBState string
	// InstallState is one of the InstallState* values for a scanner whose
	// install mode is "auto" and whose binary came from (or is being fetched by)
	// the installer, else "".
	InstallState string
	// DBUpdatedAt is when the database was last downloaded or refreshed; zero
	// when there is none or the time is unknown.
	DBUpdatedAt time.Time
	// Code is empty for a ready scanner, else one of the CodeScanner* values.
	Code string
	// Message tells an operator what to do; "" for a ready scanner.
	Message string
	// DocsURL points at the installation guide for a scanner that is not ready.
	DocsURL string
}

// Status reports osv, trivy and grype in that order. OSV needs no binary and
// is always ready (it needs outbound HTTPS at scan time). A CLI scanner is
// available when its binary resolves to an executable file and ready when its
// database is also present; the check never runs the binary.
func (r *Registry) Status() []ScannerStatus {
	return []ScannerStatus{
		{Name: scannerOSV, Available: true, Ready: true},
		r.trivy.status(r.opts.Offline, "OPENARTIFACT_TRIVY_PATH"),
		r.grype.status(r.opts.Offline, "OPENARTIFACT_GRYPE_PATH"),
	}
}

// LogStatus writes one startup line per scanner with the resolved binary,
// database directory and readiness, so a missing scanner shows up before the
// first scan.
func (r *Registry) LogStatus() {
	for _, s := range r.Status() {
		if s.Name == scannerOSV {
			continue
		}

		switch {
		case !s.Available && s.InstallState == InstallStateInstalling:
			r.log.Infow("vulnerability scanner is installed by the server (install: auto): an installed copy is reused, else the pinned release is downloaded",
				"scanner", s.Name,
				"offline", r.opts.Offline,
			)
		case !s.Available:
			r.log.Warnw("vulnerability scanner not available: scans selecting it will fail",
				"scanner", s.Name,
				"help", s.DocsURL,
			)
		default:
			r.log.Infow("vulnerability scanner available",
				"scanner", s.Name,
				"binary", s.Path,
				"cache_dir", s.CacheDir,
				"db_state", s.DBState,
				"offline", r.opts.Offline,
				"update_interval", r.opts.UpdateInterval.String(),
			)
		}
	}
}
