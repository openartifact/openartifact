// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// aliasIndex maps a content digest to the ID of a blob that was stored under a
// different ID than its own SHA-256 (a caller-supplied "sha256" metadata value
// that differs from the computed digest). Blobs whose ID is their digest never
// appear here: FilesystemStore.ExistsBySHA256 resolves those with one stat.
//
// The index is built lazily by a single walk over the ".meta" sidecar files
// the first time a digest is not found by ID, so a miss costs a map lookup
// instead of a walk over every blob on disk. Put and Delete keep it current;
// writes made by another process are only picked up after a restart, which
// can only make lookup report a miss for a blob that does exist (the caller
// then stores a duplicate), and hits are re-verified against the disk.
type aliasIndex struct {
	mu    sync.RWMutex
	built bool
	byDig map[string]string
}

func (a *aliasIndex) remove(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.built {
		return
	}

	for digest, aliasID := range a.byDig {
		if aliasID == id {
			delete(a.byDig, digest)
		}
	}
}

// lookup returns the ID of a blob stored under a non-digest ID whose content
// digest is sha256hex.
func (a *aliasIndex) lookup(store *FilesystemStore, sha256hex string) (string, bool, error) {
	a.mu.RLock()
	built := a.built
	id, ok := a.byDig[sha256hex]
	a.mu.RUnlock()

	if !built {
		a.mu.Lock()
		if !a.built {
			a.byDig = store.scanAliases()
			a.built = true
		}

		id, ok = a.byDig[sha256hex]
		a.mu.Unlock()
	}

	if !ok {
		return "", false, nil
	}

	exists, err := store.Exists(context.Background(), id)
	if err != nil || !exists {
		return "", false, err
	}

	return id, true, nil
}

// scanAliases walks the ".meta" sidecars once and returns digest -> blob ID for
// every blob whose ID differs from its content digest.
func (fsys *FilesystemStore) scanAliases() map[string]string {
	out := make(map[string]string)

	_ = filepath.WalkDir(fsys.basePath, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path == filepath.Join(fsys.basePath, filesystemStagingDir) {
			return filepath.SkipDir
		}

		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".meta") {
			return nil //nolint:nilerr // intentionally skip walk errors
		}

		//nolint:gosec // G122: path is within the storage root directory
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr // intentionally skip unreadable sidecars
		}

		var meta map[string]string
		if json.Unmarshal(data, &meta) != nil || meta["_sha256"] == "" {
			return nil //nolint:nilerr // intentionally skip malformed sidecars
		}

		rel, relErr := filepath.Rel(fsys.basePath, strings.TrimSuffix(path, ".meta"))
		if relErr != nil {
			return nil //nolint:nilerr // intentionally skip walk errors
		}

		id := strings.ReplaceAll(rel, string(os.PathSeparator), "")
		if id != meta["_sha256"] {
			out[meta["_sha256"]] = id
		}

		return nil
	})

	return out
}
