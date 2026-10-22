// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package tiering

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openartifact/openartifact/internal/storage"
)

// errAliasedBackend is returned for a backend that stores its objects at the
// same place as the primary store or another tier. A move between two such
// tiers would "copy" a blob onto itself and then delete the only copy.
var errAliasedBackend = errors.New("storage backend aliases another tier or the primary store")

// location is where a backend keeps its objects, reduced to a canonical form
// two backends can be compared by: a filesystem directory, or a key naming the
// bucket / container / repository of an object store.
type location struct {
	fsPath string // canonical directory (filesystem backends)
	key    string // object-store identity (every other kind)
}

// locate returns the canonical location of cfg. Filesystem paths are resolved
// through symlinks (the longest existing prefix is, for a directory that does
// not exist yet), so a link and its target compare equal.
func locate(cfg storage.StorageConfig) location {
	switch cfg.Type {
	case "filesystem", "":
		return location{fsPath: canonicalPath(cfg.Path)}
	case "s3", "garage", "seaweedfs":
		return location{key: "s3|" + normalizeEndpoint(cfg.Endpoint, "aws") + "|" + cfg.Bucket}
	case "gcs":
		bucket := cfg.GCSBucket
		if bucket == "" {
			bucket = cfg.Bucket
		}

		return location{key: "gcs|" + bucket}
	case "azure":
		return location{key: "azure|" + strings.ToLower(cfg.AzureAccountName) + "|" + cfg.AzureContainerName}
	case "oci":
		return location{key: "oci|" + normalizeEndpoint(cfg.Endpoint, "") + "|" + cfg.Bucket}
	default:
		return location{key: cfg.Type + "|" + cfg.Endpoint + "|" + cfg.Bucket}
	}
}

// id renders the location for equality checks and logs.
func (l location) id() string {
	if l.fsPath != "" {
		return "fs|" + l.fsPath
	}

	return l.key
}

// normalizeEndpoint lowercases an endpoint and drops its scheme and trailing
// slashes, so "https://Minio:9000/" and "minio:9000" compare equal.
func normalizeEndpoint(endpoint, fallback string) string {
	e := strings.ToLower(strings.TrimSpace(endpoint))
	if _, rest, ok := strings.Cut(e, "://"); ok {
		e = rest
	}

	if e = strings.TrimRight(e, "/"); e == "" {
		return fallback
	}

	return e
}

func absClean(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}

	return filepath.Clean(p)
}

// canonicalPath returns the absolute path of p with symlinks resolved. A path
// that does not exist yet keeps its missing tail and resolves the part that does.
func canonicalPath(p string) string {
	abs := absClean(p)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}

	rest, cur := "", abs

	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}

		rest = filepath.Join(filepath.Base(cur), rest)

		cur = parent
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
	}
}

// fsOverlap reports whether two canonical directories name the same storage:
// equal, one inside the other (the outer store's tree would contain the inner
// store's blobs), or the same inode (bind mounts).
func fsOverlap(a, b string) bool {
	if within(a, b) || within(b, a) {
		return true
	}

	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)

	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// within reports whether path is dir itself or lies below it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// claims records the locations taken so far while a snapshot is built, primary
// store first, so every later backend is checked against all earlier ones.
type claims struct {
	fs   []fsClaim
	keys map[string]string
}

type fsClaim struct {
	path  string
	owner string
}

func newClaims() *claims { return &claims{keys: map[string]string{}} }

// claim registers loc for owner, or reports the alias with an earlier claimant.
func (c *claims) claim(owner string, loc location) error {
	if loc.fsPath != "" {
		for _, f := range c.fs {
			if f.owner != owner && fsOverlap(f.path, loc.fsPath) {
				return fmt.Errorf("%w: directory %s overlaps %s", errAliasedBackend, loc.fsPath, f.path)
			}
		}

		c.fs = append(c.fs, fsClaim{path: loc.fsPath, owner: owner})

		return nil
	}

	if other, dup := c.keys[loc.key]; dup && other != owner {
		return fmt.Errorf("%w: %s", errAliasedBackend, loc.key)
	}

	c.keys[loc.key] = owner

	return nil
}
