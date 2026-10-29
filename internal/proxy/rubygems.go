// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/md5" //nolint:gosec // the compact index defines MD5 as the ETag of its files
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/pkg/compactindex"
)

// RubyGems proxy repositories mirror a gem server (default https://rubygems.org)
// through its compact index, the protocol bundler and current gem use:
//
//   - versions, info/<gem> and names are cached verbatim under their own path with
//     the repository's cache TTL and serve-stale rules, and validated as compact
//     index files before they are stored. They are served the way the upstream
//     serves them, which bundler relies on to keep its local copy current: the
//     ETag is the MD5 of the file, Repr-Digest and Digest carry its SHA-256, and a
//     Range request (bundler fetches the tail of an append-only versions file) is
//     answered with 206.
//   - gems/<file>.gem is cached under that key, immutable. The digest comes from
//     the gem's info line (checksum:<sha256>); bytes that do not match are refused
//     and never cached. An upstream without a compact index (no info for any
//     reading of the file name) is asked for the file directly and its copy is
//     cached for the repository TTL only.
//   - quick/Marshal.4.8/<file>.gemspec.rz (gem install fetches the gemspec of the
//     version it picked) and the legacy specs.4.8.gz family (gem search, gem list
//     -r) are cached verbatim after a structural check (zlib/gzip stream holding a
//     Marshal 4.8 document).

const (
	// rubygemsMaxAge is how long shared caches may reuse a public repository's
	// compact index files before revalidating (rubygems.org uses the same).
	rubygemsMaxAge = 60

	// rubygemsMarshalVerifyLimit bounds the inflated size inspected when
	// verifying a legacy specs index or a gemspec.
	rubygemsMarshalVerifyLimit = 512 << 20

	// rubygemsMD5Entries bounds the MD5 memo.
	rubygemsMD5Entries = 1024
)

// md5Memo remembers the MD5 of cached files by blob key (their SHA-256): a
// blob never changes, so the memo never goes stale and a revalidation of a large
// versions file does not read it again.
type md5Memo struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *md5Memo) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	v, ok := c.m[key]

	return v, ok
}

func (c *md5Memo) put(key, sum string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.m == nil || len(c.m) >= rubygemsMD5Entries {
		c.m = make(map[string]string)
	}

	c.m[key] = sum
}

func (s *Service) serveRubyGems(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, ok := compactindex.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	key := strings.TrimPrefix(relPath, "/")

	if p.Kind == compactindex.KindGem {
		s.serveDownload(w, r, repo, key, func(ctx context.Context) (download, error) {
			return s.locateRubyGem(ctx, repo, p, key)
		})

		return
	}

	s.serveVerbatim(w, r, repo, rubygemsDoc(key, p.Kind))
}

// rubygemsDoc is a compact index file, gemspec or legacy specs index, cached
// verbatim after a check that it is what its path names.
func rubygemsDoc(key string, kind compactindex.Kind) metaDoc {
	return metaDoc{
		key:   key,
		opts:  staticOpts(fetchOpts{verify: rubygemsVerifier(kind)}),
		limit: compactindex.MaxInfoBytes,
		what:  "gem info",
		bad:   badDocUpstream,
	}
}

// rubygemsInfoDoc is the compact index info file of a gem.
func rubygemsInfoDoc(gem string) metaDoc {
	return rubygemsDoc("info/"+gem, compactindex.KindInfo)
}

func rubygemsVerifier(kind compactindex.Kind) func(io.ReadSeeker) error {
	switch kind {
	case compactindex.KindVersions:
		return func(r io.ReadSeeker) error { return compactindex.VerifyVersions(r) }
	case compactindex.KindNames:
		return func(r io.ReadSeeker) error { return compactindex.VerifyNames(r) }
	case compactindex.KindInfo:
		return func(r io.ReadSeeker) error { return compactindex.VerifyInfo(r) }
	case compactindex.KindGemspec:
		return func(r io.ReadSeeker) error {
			zr, err := zlib.NewReader(r)
			if err != nil {
				return fmt.Errorf("gemspec is not zlib data: %w", err)
			}

			return verifyMarshal(zr)
		}
	case compactindex.KindSpecs:
		return func(r io.ReadSeeker) error {
			zr, err := gzip.NewReader(r)
			if err != nil {
				return fmt.Errorf("specs index is not gzip data: %w", err)
			}

			return verifyMarshal(zr)
		}
	case compactindex.KindGem:
	}

	return nil
}

var (
	errNotMarshal      = errors.New("not a Ruby Marshal 4.8 document")
	errMarshalTooLarge = errors.New("marshal stream too large")
)

// verifyMarshal reads the inflated stream to its end (so the container's
// checksum is verified) and checks it starts with the Marshal 4.8 header.
func verifyMarshal(r io.Reader) error {
	var head [2]byte

	if _, err := io.ReadFull(r, head[:]); err != nil || head != [2]byte{4, 8} {
		return errNotMarshal
	}

	n, err := io.Copy(io.Discard, io.LimitReader(r, rubygemsMarshalVerifyLimit))
	if err != nil {
		return fmt.Errorf("read marshal stream: %w", err)
	}

	if n >= rubygemsMarshalVerifyLimit {
		return errMarshalTooLarge
	}

	return nil
}

// rubygemsInfoLine finds the info line of version of gem in its cached info
// file. A missing gem is errNotFound, a missing version is (nil, nil).
func (s *Service) rubygemsInfoLine(ctx context.Context, repo *models.Repository, gem, version string) (*compactindex.InfoLine, error) {
	line, _, err := parseDoc(ctx, s, repo, rubygemsInfoDoc(gem), func(doc []byte, _ *resolution) (*compactindex.InfoLine, error) {
		lines, perr := compactindex.ParseInfoBytes(doc)
		if perr != nil {
			return nil, perr //nolint:wrapcheck // logged by parseDoc
		}

		if l, ok := compactindex.FindInfo(lines, version); ok {
			return &l, nil
		}

		return nil, nil //nolint:nilnil // the gem exists, the version does not
	})

	return line, err
}

// locateRubyGem finds one gem file: its upstream URL, cached only when it
// matches the checksum of its info line. A version the info does not list is
// errNotFound without a negative cache entry: the info may simply be stale. The
// file name does not say where the name ends, so every reading is looked up.
func (s *Service) locateRubyGem(ctx context.Context, repo *models.Repository, p compactindex.Path, key string) (download, error) {
	target, err := upstreamURL(repo.UpstreamURL, key)
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return download{}, errInternal
	}

	allMissing := true

	for _, sp := range compactindex.SplitFile(p.File) {
		line, lerr := s.rubygemsInfoLine(ctx, repo, sp.Name, sp.Version)

		switch {
		case errors.Is(lerr, errNotFound):
			continue
		case lerr != nil:
			return download{}, lerr
		}

		allMissing = false

		if line == nil {
			continue
		}

		if line.Checksum == "" {
			return download{url: target, opts: fetchOpts{unverified: true}}, nil
		}

		return download{url: target, opts: fetchOpts{wantSHA256: line.Checksum}}, nil
	}

	if allMissing {
		// No info for any reading: an upstream without a compact index. Its
		// file cannot be checked, so it is cached for the repository TTL only
		// and replaced on revalidation.
		return download{url: target, opts: fetchOpts{unverified: true}}, nil
	}

	return download{}, errNotFound
}

// rubygemsCompact reports whether key is a compact index file served with
// ETag/Range semantics.
func rubygemsCompact(key string) bool {
	p, ok := compactindex.ParsePath(key)

	return ok && (p.Kind == compactindex.KindVersions || p.Kind == compactindex.KindNames || p.Kind == compactindex.KindInfo)
}

// entryMD5 returns the MD5 of the cached file, the ETag the compact index
// defines.
func (s *Service) entryMD5(ctx context.Context, entry *models.ProxyCacheEntry) (string, error) {
	if sum, ok := s.gemMD5.get(entry.BlobKey); ok {
		return sum, nil
	}

	rc, err := s.blobStore.Get(ctx, entry.BlobKey)
	if err != nil {
		return "", fmt.Errorf("open cached blob: %w", err)
	}
	defer func() { base.CloseLog(rc, s.log) }()

	h := md5.New() //nolint:gosec // defined by the compact index protocol

	if _, err := iocopy.Copy(h, rc); err != nil {
		return "", fmt.Errorf("hash cached blob: %w", err)
	}

	sum := hex.EncodeToString(h.Sum(nil))
	s.gemMD5.put(entry.BlobKey, sum)

	return sum, nil
}

// writeCompactEntry serves a cached compact index file the way rubygems.org
// does, which is what bundler's incremental update expects: a content ETag,
// Repr-Digest/Digest of the whole file (also on a partial answer) and Range.
func (s *Service) writeCompactEntry(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	entry *models.ProxyCacheEntry,
	body io.Reader,
) {
	md5sum, err := s.entryMD5(r.Context(), entry)
	if err != nil {
		s.log.Errorw("failed to checksum cached compact index file", "repo", repo.Name, "blob_key", entry.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	etag := `"` + md5sum + `"`
	if base.Revalidate(w, r, repo.Public, etag, rubygemsMaxAge) {
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Accept-Ranges", "bytes")

	if raw, derr := hex.DecodeString(entry.BlobKey); derr == nil {
		digest := `sha-256="` + base64.StdEncoding.EncodeToString(raw) + `"`
		h.Set("Digest", digest)
		h.Set("Repr-Digest", digest)
	}

	start, length, status := byteRange(r, entry.SizeBytes, etag)

	switch status {
	case http.StatusRequestedRangeNotSatisfiable:
		h.Set("Content-Range", "bytes */"+strconv.FormatInt(entry.SizeBytes, 10))
		w.WriteHeader(status)

		return
	case http.StatusPartialContent:
		h.Set(
			"Content-Range",
			"bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(start+length-1, 10)+"/"+strconv.FormatInt(entry.SizeBytes, 10),
		)
	}

	h.Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(status)

	if r.Method == http.MethodHead {
		return
	}

	if start > 0 {
		if sk, ok := body.(io.Seeker); ok {
			if _, err := sk.Seek(start, io.SeekStart); err != nil {
				return
			}
		} else if _, err := io.CopyN(io.Discard, body, start); err != nil {
			return
		}
	}

	// The status line is already out: a failed copy can only abort the body.
	_, _ = iocopy.Copy(w, io.LimitReader(body, length))
}

// byteRange resolves a request's Range header against a representation of size
// bytes: the first byte, the number of bytes and the status to answer with.
// Only a single range is honored; anything else (several ranges, a malformed
// header, an If-Range that does not match) is served whole, as RFC 9110 allows.
func byteRange(r *http.Request, size int64, etag string) (int64, int64, int) {
	whole := func() (int64, int64, int) { return 0, size, http.StatusOK }

	spec, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return whole()
	}

	if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
		return whole()
	}

	first, last, ok := strings.Cut(spec, "-")
	if !ok {
		return whole()
	}

	first, last = strings.TrimSpace(first), strings.TrimSpace(last)

	if first == "" {
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 {
			return whole()
		}

		if size == 0 {
			return 0, 0, http.StatusRequestedRangeNotSatisfiable
		}

		n = min(n, size)

		return size - n, n, http.StatusPartialContent
	}

	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return whole()
	}

	end := size - 1

	if last != "" {
		e, err := strconv.ParseInt(last, 10, 64)
		if err != nil || e < start {
			return whole()
		}

		end = min(e, end)
	}

	if start >= size {
		return 0, 0, http.StatusRequestedRangeNotSatisfiable
	}

	return start, end - start + 1, http.StatusPartialContent
}

// rubygemsPackageDir is the key prefix of gem files; everything outside it is
// the compact index, gemspecs or legacy specs.
const rubygemsPackageDir = "gems/"

// RubyGemsHooks is the proxy hooks of the rubygems format.
var RubyGemsHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).serveRubyGems,
	// gems/<file>.gem, whose bytes are checked against the checksum of their
	// info line.
	Immutable: func(key string) bool {
		return strings.HasPrefix(key, rubygemsPackageDir) && strings.HasSuffix(keyName(key), ".gem")
	},
	IsMetadata: func(key string) bool { return !strings.HasPrefix(key, rubygemsPackageDir) },
	WriteEntry: func(
		s *Service, w http.ResponseWriter, r *http.Request, repo *models.Repository, entry *models.ProxyCacheEntry, key string, body io.Reader,
	) bool {
		if !rubygemsCompact(key) {
			return false
		}

		s.writeCompactEntry(w, r, repo, entry, body)

		return true
	},
	BlobPath: presign.RubyGemsBlobPath,
}
