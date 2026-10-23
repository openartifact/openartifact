// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	_ "embed" // scanners_manifest.json
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// scannersManifestJSON pins the scanner releases the server may download when
// a scanner's install mode is "auto". scripts/update-scanner-manifest.sh
// regenerates it from the versions pinned in docker/Dockerfile.
//
//go:embed scanners_manifest.json
var scannersManifestJSON []byte

var errNoBuild = errors.New("no pinned build for this platform")

// manifest lists the pinned release of each downloadable scanner.
type manifest struct {
	Scanners map[string]manifestScanner `json:"scanners"`
}

// manifestScanner is one scanner's pinned release.
type manifestScanner struct {
	Version string `json:"version"`
	// Binary is the archive entry (and installed file) name.
	Binary string `json:"binary"`
	// Platforms is keyed by "<GOOS>/<GOARCH>".
	Platforms map[string]manifestBuild `json:"platforms"`
}

// manifestBuild is one release archive.
type manifestBuild struct {
	// Path is relative to Options.DownloadBaseURL (the GitHub release path).
	Path string `json:"path"`
	// ArchiveSHA256 is the upstream checksum of the archive, from the release's
	// checksums file.
	ArchiveSHA256 string `json:"archive_sha256"`
	// BinarySHA256 is the checksum of the extracted binary; it is what a later
	// start re-verifies without the archive.
	BinarySHA256 string `json:"binary_sha256"`
}

//nolint:gochecknoglobals // parse-once cache of the compiled-in manifest
var embeddedManifest = sync.OnceValues(func() (manifest, error) {
	return parseManifest(scannersManifestJSON)
})

func parseManifest(raw []byte) (manifest, error) {
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return manifest{}, fmt.Errorf("parse scanner manifest: %w", err)
	}

	return m, nil
}

// build returns the pinned build of scanner for goos/goarch.
func (m manifest) build(scanner, goos, goarch string) (manifestScanner, manifestBuild, error) {
	s, ok := m.Scanners[scanner]
	if !ok {
		return manifestScanner{}, manifestBuild{}, fmt.Errorf("%w: %s", errNoBuild, scanner)
	}

	b, ok := s.Platforms[goos+"/"+goarch]
	if !ok {
		return manifestScanner{}, manifestBuild{}, fmt.Errorf("%w: %s %s/%s", errNoBuild, scanner, goos, goarch)
	}

	return s, b, nil
}
