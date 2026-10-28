// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/openartifact/openartifact/internal/osv"
)

// minOSVInterval is the shortest pause between OSV mirror imports: the OSV
// exports are rebuilt about hourly, and a shorter pause only adds load on the
// bucket that serves them.
const minOSVInterval = time.Hour

var (
	errOSVInterval  = errors.New("osvMirror.interval must be at least 1h")
	errOSVTempDir   = errors.New("osvMirror.tempDir must be an absolute path")
	errOSVMaxBytes  = errors.New("osvMirror.maxArchiveBytes must be positive")
	errOSVEcosystem = errors.New("osvMirror.ecosystems is empty or names an unsupported ecosystem")
)

// OSVMirrorConfig configures the local OSV mirror (see docs/security/scanning.md):
// a background worker imports the OSV export of each ecosystem into the
// database, the osv scanner matches against it, and every import re-matches the
// stored packages. OSV data is licensed CC-BY 4.0 (https://osv.dev).
type OSVMirrorConfig struct {
	// Enabled turns the mirror, its worker and the local matching on. Off by
	// default: the osv scanner then queries api.osv.dev.
	Enabled bool `yaml:"enabled"`
	// BaseURL is where the exports are fetched from ("<baseUrl>/<Ecosystem>/all.zip").
	// Point it at an internal mirror of the OSV bucket for air-gapped installs;
	// the host may be on a private network. http or https, no credentials.
	BaseURL string `yaml:"baseUrl"`
	// Ecosystems are the OSV ecosystems to mirror. The supported ones are
	// listed in docs/security/scanning.md; the others keep using api.osv.dev.
	Ecosystems []string `yaml:"ecosystems"`
	// Interval is the pause between imports, at least 1h.
	Interval time.Duration `yaml:"interval"`
	// TempDir holds the downloaded archive while it is imported (absolute).
	// Empty means the system temporary directory.
	TempDir string `yaml:"tempDir"`
	// MaxArchiveBytes caps the size of one downloaded archive.
	MaxArchiveBytes int64 `yaml:"maxArchiveBytes"`
}

// defaultOSVMirror is the mirror configuration of a fresh install: off, the
// public bucket, every supported ecosystem, a daily import.
func defaultOSVMirror() OSVMirrorConfig {
	return OSVMirrorConfig{
		BaseURL:         osv.DefaultBaseURL,
		Ecosystems:      osv.Ecosystems(),
		Interval:        24 * time.Hour,
		MaxArchiveBytes: osv.DefaultMaxArchiveBytes,
	}
}

func (c *Config) validateOSVMirror() error {
	m := c.OSVMirror
	if !m.Enabled {
		return nil
	}

	if _, err := osv.ParseBaseURL(m.BaseURL); err != nil {
		return fmt.Errorf("osvMirror.baseUrl: %w", err)
	}

	if len(m.Ecosystems) == 0 {
		return errOSVEcosystem
	}

	for _, e := range m.Ecosystems {
		if !osv.Supported(e) {
			return fmt.Errorf("%w: %q", errOSVEcosystem, e)
		}
	}

	switch {
	case m.Interval < minOSVInterval:
		return errOSVInterval
	case m.TempDir != "" && !filepath.IsAbs(m.TempDir):
		return errOSVTempDir
	case m.MaxArchiveBytes <= 0:
		return errOSVMaxBytes
	}

	return nil
}
