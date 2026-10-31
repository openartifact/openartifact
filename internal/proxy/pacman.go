// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
)

// pacmanDBSuffixes are the file name endings of a sync database: the name pacman
// asks for ("core.db", "core.files") and the archive names mirrors keep it under.
var pacmanDBSuffixes = []string{ //nolint:gochecknoglobals // read-only table
	".db", ".files",
	".db.tar.gz", ".db.tar.xz", ".db.tar.zst", ".files.tar.gz", ".files.tar.xz", ".files.tar.zst",
}

// pacmanDBMaxTTL caps how long a sync database is served without revalidation,
// whatever the repository TTL: mirrors drop the package versions a database no
// longer lists, so a database older than a sync round names files that are gone
// and every install from it fails with a 404.
const pacmanDBMaxTTL = 5 * time.Minute

// isPacmanDatabase reports whether key names a sync database of a pacman
// mirror (core/os/x86_64/core.db) or the detached signature of one.
func isPacmanDatabase(key string) bool {
	name := key[strings.LastIndexByte(key, '/')+1:]
	name = strings.TrimSuffix(name, ".sig")

	for _, suffix := range pacmanDBSuffixes {
		if len(name) > len(suffix) && strings.HasSuffix(name, suffix) {
			return true
		}
	}

	return false
}

// pacmanNotModified answers a database request of pacman. pacman stores the
// Last-Modified of a database as the mtime of its copy and asks for it again
// with If-Modified-Since, so a database the proxy has not refreshed since is
// answered with 304 instead of its megabytes. The time is the one the entry was
// last stored or revalidated, never older than the upstream copy it holds, and
// a refresh always moves it forward. It reports whether the request is answered.
func pacmanNotModified(w http.ResponseWriter, r *http.Request, entry *models.ProxyCacheEntry, key string) bool {
	if !isPacmanDatabase(key) || entry.UpdatedAt.IsZero() {
		return false
	}

	modified := entry.UpdatedAt.UTC().Truncate(time.Second)
	w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))

	since, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
	if err != nil || modified.After(since) {
		return false
	}

	w.WriteHeader(http.StatusNotModified)

	return true
}

// PacmanHooks is the proxy hooks of the pacman format. Package files have no
// immutable rule: a mirror may republish a package file name, so they use the
// repository TTL.
var PacmanHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	IsMetadata:  isPacmanDatabase,
	PackageExts: []string{".pkg.tar.zst", ".pkg.tar.xz", ".pkg.tar.gz", ".pkg.tar.bz2", ".pkg.tar.lz4", ".pkg.tar"},
	CapTTL: func(key string, ttl time.Duration) time.Duration {
		if isPacmanDatabase(key) {
			return min(ttl, pacmanDBMaxTTL)
		}

		return ttl
	},
	WriteEntry: func(
		_ *Service, w http.ResponseWriter, r *http.Request, _ *models.Repository, entry *models.ProxyCacheEntry, key string, _ io.Reader,
	) bool {
		return pacmanNotModified(w, r, entry, key)
	},
	BlobPath:    presign.PacmanBlobPath,
	Coordinates: pacmanCoordinates,
}
