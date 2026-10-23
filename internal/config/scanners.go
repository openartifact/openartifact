// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/vuln"
)

// minScannerUpdateInterval is the shortest pause between scanner database
// refreshes: the upstream databases are published every few hours, and a
// shorter pause only adds load on the registry that serves them.
const minScannerUpdateInterval = time.Hour

var (
	errScannerInterval = errors.New("scanners.updateInterval must be 0 or at least 1h")
	errScannerCacheDir = errors.New("scanners.cacheDir must be an absolute path")
	errScannerPath     = errors.New("must be a command name to look up on PATH or an absolute path")
	errScannerRepo     = errors.New("must be an OCI repository reference without spaces, not starting with '-'")
	errScannerURL      = errors.New("must be an absolute http(s) URL")
	errScannerInstall  = errors.New(`must be "none" or "auto"`)
	errScannerBaseURL  = errors.New("must be an absolute https URL without credentials, query or fragment")
)

// Install modes of a scanner binary.
const (
	// InstallNone leaves the binary to the operator (PATH or the configured path).
	InstallNone = "none"
	// InstallAuto downloads the pinned release into the cache directory when
	// the binary is neither configured nor on PATH.
	InstallAuto = "auto"
)

// DefaultScannerDownloadBaseURL serves the pinned scanner release archives.
const DefaultScannerDownloadBaseURL = "https://github.com"

// ScannersConfig configures the external vulnerability scanners (trivy, grype)
// used by on-demand scans (see docs/security/scanning.md). OSV needs none of it.
type ScannersConfig struct {
	// CacheDir is where the scanners keep their vulnerability databases
	// (<cacheDir>/trivy, <cacheDir>/grype). It must be writable unless Offline
	// is set. Empty means a directory under the user cache dir; the container
	// image sets /var/cache/openartifact/scanners.
	CacheDir string `yaml:"cacheDir"`
	// UpdateInterval is the pause between database refreshes. 0 downloads only
	// when the server starts and never again; otherwise at least 1h.
	UpdateInterval time.Duration `yaml:"updateInterval"`
	// Offline stops the server from downloading databases: scans use what
	// CacheDir already holds and fail while it holds none.
	Offline bool `yaml:"offline"`
	// DownloadBaseURL is the https origin the pinned release archives are
	// downloaded from when a scanner's install mode is "auto". A mirror must
	// serve GitHub's release paths (/<owner>/<repo>/releases/download/<tag>/<file>).
	DownloadBaseURL string       `yaml:"downloadBaseUrl"`
	Trivy           TrivyScanner `yaml:"trivy"`
	Grype           GrypeScanner `yaml:"grype"`
}

// TrivyScanner configures the trivy binary and its database sources.
type TrivyScanner struct {
	// Path is the binary: a command name looked up on PATH, or an absolute
	// path. Empty means "trivy".
	Path string `yaml:"path"`
	// Install is "none" (default) or "auto": download the pinned trivy release
	// into the cache directory when Path is empty and trivy is not on PATH.
	Install string `yaml:"install"`
	// DBRepository is an OCI repository mirroring ghcr.io/aquasecurity/trivy-db.
	// Empty is the upstream default.
	DBRepository string `yaml:"dbRepository"`
	// JavaDBRepository is an OCI repository mirroring
	// ghcr.io/aquasecurity/trivy-java-db. Empty does not download the Java
	// database (several hundred MB; only JAR scanning needs it).
	JavaDBRepository string `yaml:"javaDbRepository"`
}

// GrypeScanner configures the grype binary and its database source.
type GrypeScanner struct {
	// Path is the binary: a command name looked up on PATH, or an absolute
	// path. Empty means "grype".
	Path string `yaml:"path"`
	// Install is "none" (default) or "auto": download the pinned grype release
	// into the cache directory when Path is empty and grype is not on PATH.
	Install string `yaml:"install"`
	// DBURL is the database listing URL of a mirror (GRYPE_DB_UPDATE_URL).
	// Empty is the upstream default.
	DBURL string `yaml:"dbUrl"`
}

// VulnOptions converts the settings to the scanner subsystem's options.
func (s ScannersConfig) VulnOptions() vuln.Options {
	return vuln.Options{
		CacheDir:        s.CacheDir,
		Offline:         s.Offline,
		UpdateInterval:  s.UpdateInterval,
		DownloadBaseURL: s.DownloadBaseURL,
		Trivy: vuln.TrivyOptions{
			Path:             s.Trivy.Path,
			Install:          s.Trivy.Install == InstallAuto,
			DBRepository:     s.Trivy.DBRepository,
			JavaDBRepository: s.Trivy.JavaDBRepository,
		},
		Grype: vuln.GrypeOptions{Path: s.Grype.Path, Install: s.Grype.Install == InstallAuto, DBURL: s.Grype.DBURL},
	}
}

func (s ScannersConfig) validate() error {
	if s.UpdateInterval < 0 || (s.UpdateInterval > 0 && s.UpdateInterval < minScannerUpdateInterval) {
		return errScannerInterval
	}

	if s.CacheDir != "" && !filepath.IsAbs(s.CacheDir) {
		return errScannerCacheDir
	}

	for key, path := range map[string]string{"scanners.trivy.path": s.Trivy.Path, "scanners.grype.path": s.Grype.Path} {
		if !validScannerPath(path) {
			return fmt.Errorf("%s: %w", key, errScannerPath)
		}
	}

	for key, repo := range map[string]string{
		"scanners.trivy.dbRepository":     s.Trivy.DBRepository,
		"scanners.trivy.javaDbRepository": s.Trivy.JavaDBRepository,
	} {
		if !validOCIRepoRef(repo) {
			return fmt.Errorf("%s: %w", key, errScannerRepo)
		}
	}

	for key, mode := range map[string]string{"scanners.trivy.install": s.Trivy.Install, "scanners.grype.install": s.Grype.Install} {
		if mode != InstallNone && mode != InstallAuto {
			return fmt.Errorf("%s: %w", key, errScannerInstall)
		}
	}

	if err := validDownloadBaseURL(s.DownloadBaseURL); err != nil {
		return err
	}

	if s.Grype.DBURL != "" {
		u, err := url.Parse(s.Grype.DBURL)
		// Credentials in the URL would be logged and exposed to every process
		// that can read the scanner's environment.
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return fmt.Errorf("scanners.grype.dbUrl: %w", errScannerURL)
		}
	}

	return nil
}

// validScannerPath accepts "" (default), a bare command name, or an absolute
// path; a relative path with a separator would depend on the working directory.
func validScannerPath(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return true
	}

	return !strings.ContainsAny(p, `/\`) && !strings.HasPrefix(p, "-")
}

// validOCIRepoRef accepts "" (default) or a reference that cannot be mistaken
// for a command-line flag or split into several arguments.
func validOCIRepoRef(ref string) bool {
	if ref == "" {
		return true
	}

	return !strings.HasPrefix(ref, "-") && !strings.ContainsFunc(ref, func(r rune) bool { return r <= ' ' || r == 0x7f })
}

// validDownloadBaseURL requires an https origin (with an optional path prefix)
// that carries no credentials: archives are executed, so they must not travel
// over plain HTTP.
func validDownloadBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("scanners.downloadBaseUrl: %w", errScannerBaseURL)
	}

	return nil
}
