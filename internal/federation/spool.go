// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxPartialBytes caps the partial downloads kept between pulls.
	DefaultMaxPartialBytes int64 = 10 << 30
	// DefaultMaxPartialAge is how long an untouched partial download is kept.
	DefaultMaxPartialAge = 24 * time.Hour
	// DefaultSpoolDirName is the directory under the OS temp dir used when no
	// spool directory is configured.
	DefaultSpoolDirName = "openartifact-federation"

	partialSuffix = ".part"
	stagedSuffix  = ".staged"
)

// spool is the directory blobs of peers are downloaded into. A download lives
// in <sha256>.part until it is complete and verified, so an interrupted pull
// is resumed by the next one; the verified file is then renamed to a unique
// *.staged name that the unit's commit consumes. Both kinds are subject to the
// age and total-size caps of sweep.
type spool struct {
	dir      string
	maxBytes int64
	maxAge   time.Duration

	mu     sync.Mutex
	active map[string]chan struct{}
}

func newSpool(dir string, maxBytes int64, maxAge time.Duration) *spool {
	if dir == "" {
		dir = filepath.Join(os.TempDir(), DefaultSpoolDirName)
	}

	if maxBytes <= 0 {
		maxBytes = DefaultMaxPartialBytes
	}

	if maxAge <= 0 {
		maxAge = DefaultMaxPartialAge
	}

	return &spool{dir: dir, maxBytes: maxBytes, maxAge: maxAge, active: map[string]chan struct{}{}}
}

// acquire gives the caller exclusive use of the partial download of sha,
// waiting for another pull of the same blob to finish with it. The returned
// function releases it.
func (sp *spool) acquire(ctx context.Context, sha string) (func(), error) {
	for {
		sp.mu.Lock()

		wait, busy := sp.active[sha]
		if !busy {
			sp.active[sha] = make(chan struct{})
			sp.mu.Unlock()

			return func() {
				sp.mu.Lock()
				close(sp.active[sha])
				delete(sp.active, sha)
				sp.mu.Unlock()
			}, nil
		}

		sp.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for blob %s: %w", sha, ctx.Err())
		}
	}
}

// openPartial opens (creating it when new) the partial file of sha.
func (sp *spool) openPartial(sha string) (*os.File, error) {
	if err := os.MkdirAll(sp.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create federation spool: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(sp.dir, sha+partialSuffix), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open partial blob: %w", err)
	}

	return f, nil
}

// discard removes the partial file of sha.
func (sp *spool) discard(sha string) {
	_ = os.Remove(filepath.Join(sp.dir, sha+partialSuffix))
}

// discardEmpty removes the partial file of sha when nothing was received.
func (sp *spool) discardEmpty(sha string) {
	path := filepath.Join(sp.dir, sha+partialSuffix)
	if st, err := os.Stat(path); err == nil && st.Size() == 0 {
		_ = os.Remove(path)
	}
}

// promote turns the verified partial file of sha into a staged file the caller
// owns and returns its path.
func (sp *spool) promote(sha string) (string, error) {
	tmp, err := os.CreateTemp(sp.dir, "blob-*"+stagedSuffix)
	if err != nil {
		return "", fmt.Errorf("stage blob: %w", err)
	}

	name := tmp.Name()

	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)

		return "", fmt.Errorf("stage blob: %w", err)
	}

	if err := os.Rename(filepath.Join(sp.dir, sha+partialSuffix), name); err != nil {
		_ = os.Remove(name)

		return "", fmt.Errorf("stage blob: %w", err)
	}

	return name, nil
}

// spoolFile is one entry of the spool directory.
type spoolFile struct {
	path    string
	sha     string // set for partial files
	size    int64
	modTime time.Time
}

// list returns the spool's files; a missing directory is an empty spool.
func (sp *spool) list() []spoolFile {
	entries, err := os.ReadDir(sp.dir)
	if err != nil {
		return nil
	}

	var out []spoolFile

	for _, de := range entries {
		name := de.Name()
		if de.IsDir() || (!strings.HasSuffix(name, partialSuffix) && !strings.HasSuffix(name, stagedSuffix)) {
			continue
		}

		info, err := de.Info()
		if err != nil {
			continue
		}

		f := spoolFile{path: filepath.Join(sp.dir, name), size: info.Size(), modTime: info.ModTime()}
		if sha, ok := strings.CutSuffix(name, partialSuffix); ok {
			f.sha = sha
		}

		out = append(out, f)
	}

	return out
}

// partialBytes is the total size of the partial downloads.
func (sp *spool) partialBytes() int64 {
	var total int64

	for _, f := range sp.list() {
		if f.sha != "" {
			total += f.size
		}
	}

	return total
}

// sweep removes files untouched for longer than the maximum age, then the
// least recently written partial files until the total is within the size cap.
// A partial that a download holds is never removed. It returns the number of
// files removed.
func (sp *spool) sweep(now time.Time) int {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	var (
		removed int
		kept    []spoolFile
		total   int64
	)

	for _, f := range sp.list() {
		if _, busy := sp.active[f.sha]; f.sha != "" && busy {
			total += f.size

			continue
		}

		if now.Sub(f.modTime) > sp.maxAge {
			if os.Remove(f.path) == nil {
				removed++
			}

			continue
		}

		if f.sha != "" {
			kept = append(kept, f)
			total += f.size
		}
	}

	slices.SortFunc(kept, func(a, b spoolFile) int { return a.modTime.Compare(b.modTime) })

	for _, f := range kept {
		if total <= sp.maxBytes {
			break
		}

		if os.Remove(f.path) == nil {
			removed++
			total -= f.size
		}
	}

	return removed
}
