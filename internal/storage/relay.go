// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/urlparams"
)

// RelayRoute is the chi route pattern of the server-relayed upload endpoint.
// Backends without native presigned URLs (filesystem, OCI registry) hand out
// <PublicURL>/upload-relay/<id>/<part>?exp=<unix>&sig=<hex>; a single PUT is
// part 1 of 1. The route is mounted outside authentication: the HMAC signature
// in the query is the only credential.
const RelayRoute = "/upload-relay/{id}/{part}"

// RelayServer is implemented by stores whose staging uploads are relayed
// through this server.
type RelayServer interface {
	// RelayHandler serves RelayRoute PUT requests.
	RelayHandler() http.Handler
}

const (
	relayPathPrefix  = "/upload-relay/"
	relaySigningFile = ".signing-key"
	relayKeyLen      = 32
	relayMsgVersion  = "openartifact-relay-v1"
	relayWalkBatch   = 200
	relayMetaSuffix  = ".meta"
	relayUploadSufx  = ".upload"
	relaySealSuffix  = ".sealed"
	relayLockSuffix  = ".lock"
	relayLockPoll    = 20 * time.Millisecond
	relayPartInfix   = ".part"
	uuidLen          = 36
)

// Errors of the relay staging area.
var (
	errRelayBadID      = errors.New("relay: staging id is not a UUID")
	errRelayBadKey     = errors.New("relay: malformed signing key")
	errRelayBadPlan    = errors.New("relay: part does not match the upload plan")
	errRelayPlanDiffer = errors.New("relay: upload plan differs from the initiated one")
)

// relayMeta is the per-upload plan stored in <id>.meta.
type relayMeta struct {
	Size      int64 `json:"size"`
	PartSize  int64 `json:"part_size"`
	PartCount int   `json:"part_count"`
}

// relay is the server-relayed staging area shared by FilesystemStore and
// OCIBlobStore. A nil *relay is a staging area that is not supported.
type relay struct {
	dir       string
	publicURL string
	key       []byte
	now       func() time.Time

	// afterSeal, when set, runs once an upload is sealed and before finalize
	// waits for in-flight writers (tests only).
	afterSeal func()
}

// newRelay prepares the staging directory and signing key. It returns nil when
// publicURL is empty or the directory/key cannot be set up.
func newRelay(dir, publicURL string) *relay {
	if publicURL == "" || dir == "" {
		return nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}

	key, err := loadOrCreateRelayKey(dir)
	if err != nil {
		return nil
	}

	return &relay{
		dir:       dir,
		publicURL: strings.TrimRight(publicURL, "/"),
		key:       key,
		now:       time.Now,
	}
}

// loadOrCreateRelayKey reads <dir>/.signing-key, creating it race-safely: the
// candidate is written to a private temp file and hard-linked into place, so
// the first creator wins and every replica reads the same complete key.
func loadOrCreateRelayKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, relaySigningFile)

	if key, err := readRelayKey(path); err == nil {
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	key := make([]byte, relayKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "signing-key-*.tmp") // 0600
	if err != nil {
		return nil, fmt.Errorf("create signing key: %w", err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()

		return nil, fmt.Errorf("write signing key: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		return nil, fmt.Errorf("sync signing key: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close signing key: %w", err)
	}

	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("install signing key: %w", err)
	}

	return readRelayKey(path)
}

func readRelayKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}

	if len(key) != relayKeyLen {
		return nil, errRelayBadKey
	}

	return key, nil
}

// isStagingID reports whether id is a canonical lowercase UUID.
func isStagingID(id string) bool {
	if len(id) != uuidLen {
		return false
	}

	for i := range len(id) {
		c := id[i]

		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}

	return true
}

func (r *relay) objectPath(id string) string { return filepath.Join(r.dir, id) }
func (r *relay) uploadPath(id string) string { return filepath.Join(r.dir, id+relayUploadSufx) }
func (r *relay) metaPath(id string) string   { return filepath.Join(r.dir, id+relayMetaSuffix) }
func (r *relay) sealPath(id string) string   { return filepath.Join(r.dir, id+relaySealSuffix) }
func (r *relay) lockPath(id string) string   { return filepath.Join(r.dir, id+relayLockSuffix) }

func (r *relay) markerPath(id string, part int) string {
	return filepath.Join(r.dir, id+relayPartInfix+strconv.Itoa(part))
}

func (r *relay) sign(id string, part int, length, exp int64) string {
	mac := hmac.New(sha256.New, r.key)
	_, _ = fmt.Fprintf(mac, "%s\n%s\n%d\n%d\n%d", relayMsgVersion, id, part, length, exp)

	return hex.EncodeToString(mac.Sum(nil))
}

func (r *relay) presign(id string, part int, length int64, ttl time.Duration) PresignedPut {
	expires := r.now().Add(ttl)
	exp := expires.Unix()

	return PresignedPut{
		URL: fmt.Sprintf("%s%s%s/%d?exp=%d&sig=%s", r.publicURL, relayPathPrefix, id, part, exp,
			r.sign(id, part, length, exp)),
		Headers: map[string]string{},
		Expires: expires,
	}
}

func (r *relay) writeMeta(id string, meta relayMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encode upload plan: %w", err)
	}

	return writeFileAtomic(r.dir, r.metaPath(id), data)
}

func (r *relay) readMeta(id string) (relayMeta, error) {
	var meta relayMeta

	data, err := os.ReadFile(r.metaPath(id))
	if err != nil {
		return meta, fmt.Errorf("read upload plan: %w", err)
	}

	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, fmt.Errorf("parse upload plan: %w", err)
	}

	if meta.PartCount < 1 || meta.Size < 0 || meta.PartSize < 0 || PartCount(meta.Size, meta.PartSize) != meta.PartCount {
		return meta, errRelayBadPlan
	}

	return meta, nil
}

// StagingSupported implements StagingStore.
func (r *relay) StagingSupported() bool { return r != nil }

// MultipartSupported implements MultipartStagingStore.
func (r *relay) MultipartSupported() bool { return r != nil }

// PresignStagingPut implements StagingStore.
func (r *relay) PresignStagingPut(_ context.Context, id string, size int64, ttl time.Duration) (PresignedPut, error) {
	if r == nil {
		return PresignedPut{}, ErrPresignNotSupported
	}

	if !isStagingID(id) {
		return PresignedPut{}, errRelayBadID
	}

	if size < 0 {
		return PresignedPut{}, fmt.Errorf("%w: negative size", errRelayBadPlan)
	}

	if err := r.writeMeta(id, relayMeta{Size: size, PartSize: size, PartCount: 1}); err != nil {
		return PresignedPut{}, err
	}

	return r.presign(id, 1, size, ttl), nil
}

// InitiateMultipart implements MultipartStagingStore. The handle is empty.
func (r *relay) InitiateMultipart(_ context.Context, id string, size, partSize int64) (string, error) {
	if r == nil {
		return "", ErrPresignNotSupported
	}

	if !isStagingID(id) {
		return "", errRelayBadID
	}

	_, count, err := PlanParts(size, partSize)
	if err != nil {
		return "", err
	}

	return "", r.writeMeta(id, relayMeta{Size: size, PartSize: partSize, PartCount: count})
}

// PresignStagingPart implements MultipartStagingStore.
func (r *relay) PresignStagingPart(_ context.Context, id, _ string, number int, length int64, ttl time.Duration) (PresignedPut, error) {
	if r == nil {
		return PresignedPut{}, ErrPresignNotSupported
	}

	if !isStagingID(id) {
		return PresignedPut{}, errRelayBadID
	}

	meta, err := r.readMeta(id)
	if err != nil {
		return PresignedPut{}, err
	}

	if number < 1 || number > meta.PartCount || length != PartLength(meta.Size, meta.PartSize, number) {
		return PresignedPut{}, errRelayBadPlan
	}

	return r.presign(id, number, length, ttl), nil
}

// CompleteMultipart implements MultipartStagingStore.
func (r *relay) CompleteMultipart(ctx context.Context, id, _ string, size, partSize int64) error {
	if r == nil {
		return ErrPresignNotSupported
	}

	if !isStagingID(id) {
		return errRelayBadID
	}

	if isRegular(r.objectPath(id)) {
		return nil
	}

	meta, err := r.readMeta(id)
	if err != nil {
		return fmt.Errorf("%w: unknown upload: %w", ErrMultipartIncomplete, err)
	}

	if meta.Size != size || meta.PartSize != partSize {
		return errRelayPlanDiffer
	}

	if missing := r.missingParts(id, meta); len(missing) > 0 {
		return fmt.Errorf("%w: missing parts %v", ErrMultipartIncomplete, missing)
	}

	return r.finalize(ctx, id, meta.PartCount)
}

func isRegular(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

func (r *relay) missingParts(id string, meta relayMeta) []int {
	var missing []int

	for n := 1; n <= meta.PartCount; n++ {
		if !isRegular(r.markerPath(id, n)) {
			missing = append(missing, n)
		}
	}

	return missing
}

// finalize publishes <id>.upload as <id> without overwriting an existing
// object; concurrent finalizers all succeed.
//
// The object shares the partial upload's inode, so no writer may touch it once
// it is published. finalize therefore seals the upload first (every later part
// PUT answers 409), then waits for the PUTs already writing (they hold the
// upload's shared lock) before it links. From the link on the bytes never
// change, so whatever the caller verifies is exactly what is promoted.
func (r *relay) finalize(ctx context.Context, id string, parts int) error {
	if isRegular(r.objectPath(id)) {
		return nil
	}

	sealed, err := r.seal(id)
	if err != nil {
		return err
	}

	if r.afterSeal != nil {
		r.afterSeal()
	}

	unlock, err := r.lockExclusive(ctx, id)
	if err != nil {
		r.unseal(id, sealed)

		return err
	}

	defer unlock()

	err = os.Link(r.uploadPath(id), r.objectPath(id))
	if err != nil && !errors.Is(err, os.ErrExist) {
		if isRegular(r.objectPath(id)) {
			return nil // another finalizer got there first
		}

		r.unseal(id, sealed)

		return fmt.Errorf("finalize upload: %w", err)
	}

	_ = syncDir(r.dir)
	// The object keeps the inode; dropping the upload name means a late part
	// PUT can no longer reach it.
	_ = os.Remove(r.uploadPath(id))

	for n := 1; n <= parts; n++ {
		_ = os.Remove(r.markerPath(id, n))
	}

	return nil
}

// seal marks the upload as being completed. created is true when this call
// made the marker.
func (r *relay) seal(id string) (bool, error) {
	f, err := os.OpenFile(r.sealPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("seal upload: %w", err)
	}

	return true, f.Close()
}

// unseal reopens an upload whose completion failed before the object was
// published, so the client can go on.
func (r *relay) unseal(id string, created bool) {
	if created && !isRegular(r.objectPath(id)) {
		_ = os.Remove(r.sealPath(id))
	}
}

// lockExclusive waits until no part PUT is writing.
func (r *relay) lockExclusive(ctx context.Context, id string) (func(), error) {
	tick := time.NewTicker(relayLockPoll)
	defer tick.Stop()

	for {
		unlock, held, err := tryLockExclusive(r.lockPath(id))
		if err != nil {
			return nil, err
		}

		if !held {
			return unlock, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for in-flight part uploads: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

func stagedVersion(info os.FileInfo) string {
	return fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano())
}

// StatStaging implements StagingStore.
func (r *relay) StatStaging(_ context.Context, id string) (StagedObject, error) {
	if r == nil {
		return StagedObject{}, ErrPresignNotSupported
	}

	if !isStagingID(id) {
		return StagedObject{}, ErrStagingNotFound
	}

	info, err := os.Stat(r.objectPath(id))
	if err != nil || !info.Mode().IsRegular() {
		return StagedObject{}, ErrStagingNotFound
	}

	return StagedObject{Size: info.Size(), Version: stagedVersion(info)}, nil
}

// OpenStaging implements StagingStore.
func (r *relay) OpenStaging(_ context.Context, id, version string) (io.ReadCloser, error) {
	f, err := r.openVerified(id, version)
	if err != nil {
		return nil, err
	}

	return f, nil
}

// openVerified opens the staging object and checks the opened file (not the
// path) is exactly version.
func (r *relay) openVerified(id, version string) (*os.File, error) {
	if r == nil {
		return nil, ErrPresignNotSupported
	}

	if !isStagingID(id) {
		return nil, ErrStagingNotFound
	}

	f, err := os.Open(r.objectPath(id))
	if err != nil {
		return nil, ErrStagingNotFound
	}

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()

		return nil, ErrStagingNotFound
	}

	if stagedVersion(info) != version {
		_ = f.Close()

		return nil, ErrStagingChanged
	}

	return f, nil
}

// DeleteStaging implements StagingStore.
func (r *relay) DeleteStaging(_ context.Context, id string) error {
	if r == nil {
		return ErrPresignNotSupported
	}

	if !isStagingID(id) {
		return errRelayBadID
	}

	var firstErr error

	remove := func(path string) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = fmt.Errorf("remove staging file: %w", err)
		}
	}

	// The plan goes first: a part PUT arriving now finds no upload.
	remove(r.metaPath(id))
	remove(r.objectPath(id))
	remove(r.uploadPath(id))
	remove(r.sealPath(id))
	remove(r.lockPath(id))

	if matches, err := filepath.Glob(filepath.Join(r.dir, id+relayPartInfix+"*")); err == nil {
		for _, m := range matches {
			remove(m)
		}
	}

	return firstErr
}

// stagingFileID extracts the staging id from a file name in the staging dir.
func stagingFileID(name string) (string, bool) {
	if len(name) < uuidLen {
		return "", false
	}

	id, rest := name[:uuidLen], name[uuidLen:]
	if !isStagingID(id) {
		return "", false
	}

	switch {
	case rest == "", rest == relayMetaSuffix, rest == relayUploadSufx, rest == relaySealSuffix, rest == relayLockSuffix:
		return id, true
	case strings.HasPrefix(rest, relayPartInfix):
		_, err := strconv.Atoi(rest[len(relayPartInfix):])

		return id, err == nil
	}

	return "", false
}

// WalkStaging implements StagingStore. An id is reported when every file of it
// (object, partial upload, part markers, plan) is older than olderThan.
func (r *relay) WalkStaging(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	if r == nil {
		return ErrPresignNotSupported
	}

	newest := make(map[string]time.Time)

	dir, err := os.Open(r.dir)
	if err != nil {
		return fmt.Errorf("open staging dir: %w", err)
	}

	defer func() { _ = dir.Close() }()

	for {
		entries, rerr := dir.ReadDir(relayWalkBatch)
		for _, e := range entries {
			id, ok := stagingFileID(e.Name())
			if !ok || e.IsDir() {
				continue
			}

			info, ierr := e.Info()
			if ierr != nil {
				continue
			}

			if t := info.ModTime(); t.After(newest[id]) {
				newest[id] = t
			}
		}

		if errors.Is(rerr, io.EOF) {
			break
		}

		if rerr != nil {
			return fmt.Errorf("read staging dir: %w", rerr)
		}
	}

	batch := make([]string, 0, relayWalkBatch)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}

		err := fn(batch)
		batch = make([]string, 0, relayWalkBatch)

		return err
	}

	for id, t := range newest {
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("walk staging: %w", cerr)
		}

		if !t.Before(olderThan) {
			continue
		}

		if batch = append(batch, id); len(batch) == relayWalkBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}

	return flush()
}

// RelayHandler implements RelayServer.
func (r *relay) RelayHandler() http.Handler {
	if r == nil {
		return http.NotFoundHandler()
	}

	return http.HandlerFunc(r.servePut)
}

func (r *relay) servePut(w http.ResponseWriter, req *http.Request) {
	vars := urlparams.Vars(req)
	id := vars["id"]

	part, perr := strconv.Atoi(vars["part"])
	if !isStagingID(id) || perr != nil {
		http.NotFound(w, req)

		return
	}

	meta, err := r.readMeta(id)
	if err != nil || part < 1 || part > meta.PartCount {
		http.NotFound(w, req)

		return
	}

	length := PartLength(meta.Size, meta.PartSize, part)

	q := req.URL.Query()
	exp, eerr := strconv.ParseInt(q.Get("exp"), 10, 64)
	sig, serr := hex.DecodeString(q.Get("sig"))

	want, _ := hex.DecodeString(r.sign(id, part, length, exp))
	if eerr != nil || serr != nil || !hmac.Equal(sig, want) {
		http.Error(w, "forbidden", http.StatusForbidden)

		return
	}

	if r.now().Unix() > exp {
		http.Error(w, "expired", http.StatusGone)

		return
	}

	if req.ContentLength < 0 {
		http.Error(w, "content length required", http.StatusLengthRequired)

		return
	}

	if req.ContentLength != length {
		http.Error(w, "wrong content length", http.StatusBadRequest)

		return
	}

	etag, status := r.putPart(id, part, meta, length, req.Body)
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)

		return
	}

	if len(r.missingParts(id, meta)) == 0 {
		if err := r.finalize(req.Context(), id, meta.PartCount); err != nil {
			http.Error(w, "finalize failed", http.StatusInternalServerError)

			return
		}
	}

	w.Header().Set("ETag", `"`+etag+`"`)
	w.WriteHeader(http.StatusOK)
}

// putPart writes one part while holding the upload's shared lock, so a
// concurrent finalize waits for it, and refuses the part once the upload is
// sealed or published (409).
func (r *relay) putPart(id string, part int, meta relayMeta, length int64, body io.Reader) (string, int) {
	unlock, err := lockShared(r.lockPath(id))
	if err != nil {
		return "", http.StatusInternalServerError
	}

	defer unlock()

	if isRegular(r.sealPath(id)) || isRegular(r.objectPath(id)) || !isRegular(r.metaPath(id)) {
		return "", http.StatusConflict
	}

	etag, err := r.writePart(id, part, meta, length, body)
	if err != nil {
		return "", http.StatusBadRequest
	}

	return etag, http.StatusOK
}

// writePart streams exactly length bytes of body to the part's offset of the
// partial upload, syncs it and records the part marker. It returns the part's
// SHA-256 as the ETag.
func (r *relay) writePart(id string, part int, meta relayMeta, length int64, body io.Reader) (string, error) {
	f, err := os.OpenFile(r.uploadPath(id), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return "", fmt.Errorf("open partial upload: %w", err)
	}

	defer func() { _ = f.Close() }()

	h := sha256.New()
	off := int64(part-1) * meta.PartSize

	n, err := io.Copy(io.NewOffsetWriter(f, off), io.TeeReader(io.LimitReader(body, length), h))
	if err != nil {
		return "", fmt.Errorf("write part: %w", err)
	}

	if n != length {
		return "", io.ErrUnexpectedEOF
	}

	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("sync part: %w", err)
	}

	if err := os.WriteFile(r.markerPath(id, part), nil, 0o600); err != nil {
		return "", fmt.Errorf("record part: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
