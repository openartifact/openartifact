// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	pkgnix "github.com/openartifact/openartifact/pkg/nix"
)

// A nix proxy mirrors a binary cache (cache.nixos.org by default) by path. Four
// kinds of file exist:
//
//   - <hash>.narinfo, passed through byte for byte so the upstream signatures
//     stay valid for the client, which checks them against the keys it trusts
//     (the proxy has no keys configured and never verifies them). It is cached
//     for the repository TTL, a 404 for the negative TTL. A narinfo is cached
//     only when it parses, describes the store path its file name names, and
//     points to a NAR below nar/ (see nixNarinfoFetchOpts).
//   - nar/<file>, immutable. A NAR is cached only when its body has the
//     FileHash and FileSize of the narinfo that pointed to it; the proxy learns
//     that expectation when it caches the narinfo (nixNarSidecar). The NAR file
//     name is not the file's hash in general (cache.nixos.org names zstd NARs
//     after the NarHash), so the name is trusted only when it is the hash.
//   - nix-cache-info, a metadata file passed through the cache.
//   - anything else (build logs, realisations, ...), cached by path for the
//     repository TTL.
//
// Narinfo and nix-cache-info are capped at the metadata size limit, a NAR at
// the body size limit: a larger NAR is refused.

const (
	nixNarinfoExt      = ".narinfo"
	nixCacheInfoKey    = "nix-cache-info"
	nixNarDir          = "nar/"
	nixNarinfoMIME     = "text/x-nix-narinfo"
	nixSidecarPrefix   = "nixnar\\"
	nixSidecarMaxBytes = 256
)

var (
	errNixNarinfoURL  = errors.New("narinfo does not point to a NAR below nar/")
	errNixNarinfoPath = errors.New("narinfo describes another store path than its file name")
	errNixNarSize     = errors.New("NAR does not have the size its narinfo lists")
	errNixSidecar     = errors.New("invalid NAR expectation record")
)

// nixNarinfoKey reports whether key is the narinfo of a store path
// (<32 character hash>.narinfo at the root of the cache).
func nixNarinfoKey(key string) bool {
	hash, ok := strings.CutSuffix(key, nixNarinfoExt)

	return ok && pkgnix.ValidStoreHash(hash)
}

// nixNarKey reports whether key is a NAR file.
func nixNarKey(key string) bool {
	return strings.HasPrefix(key, nixNarDir) && len(key) > len(nixNarDir)
}

// nixMetadataKey reports whether key is a narinfo (or its listing), the
// cache-info file or any other small document of a binary cache.
func nixMetadataKey(key string) bool {
	return !nixNarKey(key)
}

// nixSidecarKey is the cache key under which the proxy records what the NAR at
// url must look like. A client cannot request it: CleanPath refuses backslashes.
func nixSidecarKey(url string) string { return nixSidecarPrefix + url }

// nixNarExpectation is what a narinfo says about the file at its URL.
type nixNarExpectation struct {
	sha256 string // lowercase hex
	size   int64  // 0 when the narinfo does not list it
}

// nixExpectation returns the digest and size of the file info.URL names: the
// FileHash/FileSize pair, or the NarHash/NarSize pair of an uncompressed NAR
// whose narinfo omits them. ok is false when the narinfo lists neither.
func nixExpectation(info *pkgnix.NarInfo) (nixNarExpectation, bool) {
	hash, size := info.FileHash, info.FileSize

	if hash == "" && info.Compression == "none" {
		hash, size = info.NarHash, info.NarSize
	}

	if hash == "" {
		return nixNarExpectation{}, false
	}

	sum, err := pkgnix.ParseSHA256(hash)
	if err != nil {
		return nixNarExpectation{}, false
	}

	return nixNarExpectation{sha256: sum.Hex(), size: max(size, 0)}, true
}

func (e nixNarExpectation) record() []byte {
	return []byte(e.sha256 + " " + strconv.FormatInt(e.size, 10))
}

func parseNixExpectation(b []byte) (nixNarExpectation, error) {
	sum, size, ok := strings.Cut(string(b), " ")
	if !ok || len(sum) != 64 {
		return nixNarExpectation{}, errNixSidecar
	}

	n, err := strconv.ParseInt(size, 10, 64)
	if err != nil || n < 0 {
		return nixNarExpectation{}, errNixSidecar
	}

	return nixNarExpectation{sha256: sum, size: n}, nil
}

// fetchNix is fetch for a nix upstream.
func (s *Service) fetchNix(ctx context.Context, repo *models.Repository, key, target string) (*models.ProxyCacheEntry, error) {
	switch {
	case nixNarinfoKey(key):
		return s.fetchNarinfo(ctx, repo, key, target)
	case nixNarKey(key):
		o, err := s.nixNarFetchOpts(ctx, repo, key)
		if err != nil {
			return nil, err
		}

		return s.fetchURL(ctx, repo, key, target, o)
	}

	return s.fetchURL(ctx, repo, key, target, fetchOpts{})
}

// fetchNarinfo fetches and caches a narinfo, then records what its NAR must
// look like. When that record cannot be written the narinfo is dropped again:
// the client would otherwise be given a NAR location the proxy refuses to serve.
func (s *Service) fetchNarinfo(ctx context.Context, repo *models.Repository, key, target string) (*models.ProxyCacheEntry, error) {
	var info *pkgnix.NarInfo

	entry, err := s.fetchURL(ctx, repo, key, target, fetchOpts{
		contentType: nixNarinfoMIME,
		verify: func(spool io.ReadSeeker) error {
			var verr error

			info, verr = verifyNarinfo(spool, key)

			return verr
		},
	})
	if err != nil {
		return nil, err
	}

	if exp, ok := nixExpectation(info); ok {
		if serr := s.writeNixSidecar(ctx, repo, info.URL, exp); serr != nil {
			s.log.Errorw("failed to record the NAR expectation of a narinfo", "repo", repo.Name, "path", key, "error", serr)

			if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, key); derr != nil {
				s.log.Errorw("failed to drop proxy cache entry", "repo", repo.Name, "path", key, "error", derr)
			}

			return nil, errInternal
		}
	}

	return entry, nil
}

// verifyNarinfo parses the spooled narinfo of the store path key names and
// checks it describes that path and a NAR the proxy can serve.
func verifyNarinfo(spool io.ReadSeeker, key string) (*pkgnix.NarInfo, error) {
	data, err := io.ReadAll(io.LimitReader(spool, pkgnix.MaxNarInfoBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read narinfo: %w", err)
	}

	info, err := pkgnix.ParseNarInfo(data)
	if err != nil {
		return nil, fmt.Errorf("parse narinfo: %w", err)
	}

	if info.StoreHash()+nixNarinfoExt != key {
		return nil, errNixNarinfoPath
	}

	if clean, ok := CleanPath(info.URL); !ok || clean != info.URL || !nixNarKey(clean) {
		return nil, errNixNarinfoURL
	}

	return info, nil
}

// writeNixSidecar stores the expectation of the NAR at url as a cache entry of
// its own, valid as long as an immutable file.
func (s *Service) writeNixSidecar(ctx context.Context, repo *models.Repository, url string, exp nixNarExpectation) error {
	body := exp.record()
	digest := sha256.Sum256(body)
	sum := hex.EncodeToString(digest[:])
	now := time.Now()

	entry := &models.ProxyCacheEntry{
		RepositoryID: repo.ID,
		CacheKey:     nixSidecarKey(url),
		BlobKey:      sum,
		ContentType:  "text/plain",
		StatusCode:   http.StatusOK,
		SizeBytes:    int64(len(body)),
		ExpiresAt:    now.Add(immutableTTL),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	return s.persist(ctx, bytes.NewReader(body), sum, "text/plain", entry)
}

// readNixSidecar returns the recorded expectation of the NAR at url; ok is
// false when none is on record.
func (s *Service) readNixSidecar(ctx context.Context, repo *models.Repository, url string) (nixNarExpectation, bool, error) {
	entry, err := s.db.GetProxyCacheEntry(ctx, repo.ID, nixSidecarKey(url))
	if err != nil {
		return nixNarExpectation{}, false, fmt.Errorf("read NAR expectation: %w", err)
	}

	if entry == nil || entry.BlobKey == "" || !entry.ExpiresAt.After(time.Now()) {
		return nixNarExpectation{}, false, nil
	}

	rc, err := s.blobStore.Get(ctx, entry.BlobKey)
	if err != nil {
		// The blob is gone: as if never recorded; the narinfo refetch writes it again.
		return nixNarExpectation{}, false, nil //nolint:nilerr // a missing record is "no expectation"
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, nixSidecarMaxBytes))
	if err != nil {
		return nixNarExpectation{}, false, fmt.Errorf("read NAR expectation: %w", err)
	}

	exp, perr := parseNixExpectation(data)
	if perr != nil {
		return nixNarExpectation{}, false, nil //nolint:nilerr // a damaged record is "no expectation"
	}

	return exp, true, nil
}

// nixNarFetchOpts returns the options for fetching the NAR key: its body must
// match the digest and size its narinfo listed. Without a record the NAR is
// served only when its name is the nixbase32 of its own sha256, which the body
// then has to match; anything else is refused (errVerify).
func (s *Service) nixNarFetchOpts(ctx context.Context, repo *models.Repository, key string) (fetchOpts, error) {
	exp, ok, err := s.readNixSidecar(ctx, repo, key)
	if err != nil {
		s.log.Errorw("failed to read the NAR expectation", "repo", repo.Name, "path", key, "error", err)
		return fetchOpts{}, errInternal
	}

	if !ok {
		named, perr := pkgnix.ParseNarFile(strings.TrimPrefix(key, nixNarDir))
		if perr != nil {
			s.log.Warnw("NAR has no narinfo on record and is not named after its hash, refusing", "repo", repo.Name, "path", key)
			return fetchOpts{}, errVerify
		}

		exp = nixNarExpectation{sha256: named.Hex()}
	}

	return fetchOpts{
		wantSHA256: exp.sha256,
		verify: func(spool io.ReadSeeker) error {
			if exp.size == 0 {
				return nil
			}

			size, err := spool.Seek(0, io.SeekEnd)
			if err != nil {
				return fmt.Errorf("size spool: %w", err)
			}

			if size != exp.size {
				return fmt.Errorf("%w: %d bytes, narinfo lists %d", errNixNarSize, size, exp.size)
			}

			return nil
		},
	}, nil
}
