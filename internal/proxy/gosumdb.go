// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb"
	"golang.org/x/mod/sumdb/dirhash"
	modzip "golang.org/x/mod/zip"

	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Go module proxies are verified against the Go checksum database
// (sum.golang.org by default) before a module file is cached: the signed tree
// head, the record's inclusion proof and the tiles are checked by
// golang.org/x/mod/sumdb exactly as the go command does, and the h1: hash of
// the downloaded .zip / .mod must equal the recorded go.sum line.
//
//   - The database does not know the module version (404/410): a private or
//     not yet indexed module. Nothing can be checked against the database, so
//     only the structure is verified (the zip must satisfy the rules the go
//     command enforces when it unpacks it, a go.mod must declare a module),
//     and the file is cached for the repository TTL instead of as immutable,
//     so it is fetched and checked again once the database has the record.
//   - The database cannot be reached or misbehaves: the file is refused and
//     not cached (fail closed); a cached copy keeps being served.
//   - GOSUMDB=off disables the lookup (structure only), for deployments
//     whose proxies mirror modules the database cannot be reached for.
//
// The latest signed tree head is held in memory only: after a restart the
// first lookup trusts the database's current head again, which the go command
// of every client then re-verifies against its own go.sum / GOSUMDB state.

const (
	sumdbName       = "sum.golang.org"
	sumdbKey        = "sum.golang.org+033de0ae+Ac4zctda0e5eza+HJyk9SxEdh+s3Ux18htTTAD8OuAn8"
	sumdbAltName    = "sum.golang.google.cn"
	sumdbReqTimeout = time.Minute

	// maxSumdbBody bounds one lookup/tile response.
	maxSumdbBody = 16 << 20

	// sumdbCacheCap bounds the in-memory lookup/tile cache. When it fills up
	// the cache is dropped as a whole: entries are cheap to refetch.
	sumdbCacheCap = 32 << 20
)

var (
	errSumdbMismatch = errors.New("does not match the checksum database")
	errSumdbMissing  = errors.New("checksum database record lacks the file hash")
	errBadGoMod      = errors.New("go.mod declares no module")
	errGoModTooLarge = errors.New("go.mod too large")
	errNotZipFile    = errors.New("spooled body is not a file")
	errSumdbStatus   = errors.New("unexpected checksum database status")
	errSumdbTooLarge = errors.New("checksum database response too large")
	errSumdbConfig   = errors.New("unknown checksum database config file")
)

// sumDB verifies module hashes against one checksum database.
type sumDB struct {
	name    string
	vkey    string
	baseURL string
	client  *http.Client
	log     *logger.Logger

	mu        sync.Mutex
	latest    []byte
	cache     map[string][]byte
	cacheSize int
}

// newSumDB builds the verifier described by spec, the value of the go
// command's GOSUMDB variable: empty (the default database), "off" (nil: no
// lookups), or "<name>+<hash>+<key> [url]" for another database. An invalid
// spec is reported and the default database used, never silently dropped.
func newSumDB(spec string, client *http.Client, log *logger.Logger) *sumDB {
	fields := strings.Fields(spec)

	if len(fields) == 0 {
		return &sumDB{name: sumdbName, vkey: sumdbKey, baseURL: "https://" + sumdbName, client: client, log: log}
	}

	if fields[0] == "off" {
		return nil
	}

	var name, vkey, host string

	switch {
	case fields[0] == sumdbName || fields[0] == sumdbAltName:
		name, vkey, host = sumdbName, sumdbKey, fields[0]
	case strings.Count(fields[0], "+") == 2:
		name, vkey = fields[0][:strings.IndexByte(fields[0], '+')], fields[0]
		host = name
	default:
		log.Errorw("invalid GOSUMDB, using the default checksum database", "value", spec)
		return newSumDB("", client, log)
	}

	baseURL := "https://" + host
	if len(fields) > 1 {
		baseURL = fields[1]
		if !strings.Contains(baseURL, "://") {
			baseURL = "https://" + baseURL
		}
	}

	return &sumDB{name: name, vkey: vkey, baseURL: strings.TrimRight(baseURL, "/"), client: client, log: log}
}

// lookup returns the go.sum lines recorded for module@version. known is false
// when the database has no such record. A fresh sumdb.Client serves each
// lookup: it memoizes failures forever, which a long-lived proxy must not.
func (d *sumDB) lookup(module, version string) ([]string, bool, error) {
	ops := &sumdbOps{db: d}

	lines, err := sumdb.NewClient(ops).Lookup(module, version)
	if err != nil {
		if st := ops.lookupStatus.Load(); st == http.StatusNotFound || st == http.StatusGone {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("checksum database lookup: %w", err)
	}

	return lines, true, nil
}

// sumdbOps is the sumdb.ClientOps of one lookup over the shared state of d.
type sumdbOps struct {
	db *sumDB
	// lookupStatus is the HTTP status of a failed "/lookup/" request.
	lookupStatus atomic.Int32
}

func (o *sumdbOps) ReadRemote(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sumdbReqTimeout)
	defer cancel()

	target := o.db.baseURL + path

	if err := netutil.ValidateOutboundURLContext(ctx, target); err != nil {
		return nil, fmt.Errorf("checksum database url rejected: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build checksum database request: %w", err)
	}

	req.Header.Set("User-Agent", "OpenArtifact-Proxy")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := o.db.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		if strings.HasPrefix(path, "/lookup/") {
			o.lookupStatus.Store(int32(resp.StatusCode)) //nolint:gosec // an HTTP status
		}

		return nil, fmt.Errorf("fetch %s: %w: %s", path, errSumdbStatus, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSumdbBody+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	if len(body) > maxSumdbBody {
		return nil, fmt.Errorf("fetch %s: %w", path, errSumdbTooLarge)
	}

	return body, nil
}

func (o *sumdbOps) ReadConfig(file string) ([]byte, error) {
	d := o.db

	switch file {
	case "key":
		return []byte(d.vkey), nil
	case d.name + "/latest":
		d.mu.Lock()
		defer d.mu.Unlock()

		return bytes.Clone(d.latest), nil
	}

	return nil, fmt.Errorf("%w %q", errSumdbConfig, file)
}

func (o *sumdbOps) WriteConfig(file string, oldData, newData []byte) error {
	d := o.db
	if file != d.name+"/latest" {
		return fmt.Errorf("%w %q", errSumdbConfig, file)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if !bytes.Equal(d.latest, oldData) {
		return sumdb.ErrWriteConflict
	}

	d.latest = bytes.Clone(newData)

	return nil
}

func (o *sumdbOps) ReadCache(file string) ([]byte, error) {
	d := o.db

	d.mu.Lock()
	defer d.mu.Unlock()

	if data, ok := d.cache[file]; ok {
		return bytes.Clone(data), nil
	}

	return nil, os.ErrNotExist
}

func (o *sumdbOps) WriteCache(file string, data []byte) {
	d := o.db

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cache == nil || d.cacheSize+len(data) > sumdbCacheCap {
		d.cache, d.cacheSize = map[string][]byte{}, 0
	}

	if old, ok := d.cache[file]; ok {
		d.cacheSize -= len(old)
	}

	d.cache[file] = bytes.Clone(data)
	d.cacheSize += len(data)
}

func (o *sumdbOps) Log(msg string) { o.db.log.Debugw("checksum database", "msg", msg) }

func (o *sumdbOps) SecurityError(msg string) {
	o.db.log.Errorw("checksum database security error", "msg", msg)
}

// check compares the h1: hash of one file of module@version (suffix "" for
// the zip, "/go.mod" for the go.mod) with the database's record. It reports
// whether the record vouched for the file: false when the database does not
// know the version, so the caller must not pin the file as immutable. With the
// lookup switched off (GOSUMDB=off, d == nil) the operator accepts structure
// checks alone and the file counts as vouched.
func (d *sumDB) check(module, version, suffix, got string) (bool, error) {
	if d == nil {
		return true, nil
	}

	// The client returns only the record lines for the exact version asked
	// for: the "/go.mod" form selects the go.mod hash.
	lines, known, err := d.lookup(module, version+suffix)
	if err != nil {
		return false, err
	}

	if !known {
		d.log.Debugw("module version unknown to the checksum database, structure verified only",
			"module", module, "version", version)

		return false, nil
	}

	prefix := module + " " + version + suffix + " "

	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			if rest != got {
				return false, fmt.Errorf("%s@%s%s: %w", module, version, suffix, errSumdbMismatch)
			}

			return true, nil
		}
	}

	return false, fmt.Errorf("%s@%s%s: %w", module, version, suffix, errSumdbMissing)
}

// verifyGoMod returns the verify hook for a .mod body; vouched is set when the
// checksum database confirmed the hash.
func (s *Service) verifyGoMod(modulePath, version string, vouched *bool) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		data, err := io.ReadAll(io.LimitReader(spool, modzip.MaxGoMod+1))
		if err != nil {
			return fmt.Errorf("read go.mod: %w", err)
		}

		if len(data) > modzip.MaxGoMod {
			return errGoModTooLarge
		}

		if modfile.ModulePath(data) == "" {
			return errBadGoMod
		}

		got, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		})
		if err != nil {
			return fmt.Errorf("hash go.mod: %w", err)
		}

		ok, err := s.sumdb.check(modulePath, version, "/go.mod", got)
		*vouched = ok

		return err
	}
}

// verifyGoZip returns the verify hook for a .zip body: the zip must pass the
// checks the go command applies when unpacking it (all files below
// <module>@<version>/, size limits, valid names, one root go.mod) and hash to
// the checksum database's record.
func (s *Service) verifyGoZip(modulePath, version string, vouched *bool) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		f, ok := spool.(interface{ Name() string })
		if !ok {
			return errNotZipFile
		}

		cf, err := modzip.CheckZip(module.Version{Path: modulePath, Version: version}, f.Name())
		if err != nil {
			return fmt.Errorf("check module zip: %w", err)
		}

		if err := cf.Err(); err != nil {
			return fmt.Errorf("check module zip: %w", err)
		}

		got, err := dirhash.HashZip(f.Name(), dirhash.Hash1)
		if err != nil {
			return fmt.Errorf("hash module zip: %w", err)
		}

		ok, cerr := s.sumdb.check(modulePath, version, "", got)
		*vouched = ok

		return cerr
	}
}
