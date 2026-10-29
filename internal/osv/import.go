// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package osv

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// DefaultBaseURL is the public bucket the OSV project publishes the
	// per-ecosystem exports in ("<base>/<Ecosystem>/all.zip").
	DefaultBaseURL = "https://osv-vulnerabilities.storage.googleapis.com"

	// DefaultMaxArchiveBytes bounds one downloaded archive.
	DefaultMaxArchiveBytes int64 = 2 << 30

	// downloadTimeout bounds one archive download.
	downloadTimeout = 30 * time.Minute
	// maxRecordBytes bounds one decompressed record of an archive.
	maxRecordBytes = 16 << 20
	// maxDecompressRatio bounds the decompressed records of one archive as a
	// whole to this multiple of the archive size cap. JSON records compress
	// to a tenth or less; the margin is for repetitive ones. Without it only
	// the single record and the record count are bounded, which allows a
	// small download to expand to terabytes.
	maxDecompressRatio = 32
	// maxRecords bounds the records read from one archive.
	maxRecords = 2_000_000
	// batchSize is how many advisories one database transaction stores.
	batchSize = 500
	// maxLoggedSkips bounds the malformed records an import logs one by one.
	maxLoggedSkips = 5
)

// ErrArchiveTooLarge means a download or an archive entry exceeds its size cap.
var ErrArchiveTooLarge = errors.New("osv archive exceeds the size limit")

// ErrUnsupportedEcosystem means an ecosystem the local mirror cannot match.
var ErrUnsupportedEcosystem = errors.New("osv ecosystem is not supported by the local mirror")

// ErrChecksumMismatch means the downloaded archive does not match the checksum
// the source sent with it.
var ErrChecksumMismatch = errors.New("osv archive does not match its checksum")

// ErrBadStatus means the source answered with an unexpected HTTP status.
var ErrBadStatus = errors.New("osv source returned an unexpected status")

// ErrInvalidBaseURL means the configured source URL is not usable.
var ErrInvalidBaseURL = errors.New("invalid osv source URL")

// ParseBaseURL validates the configured source URL: http or https, a host, no
// credentials, no query or fragment. A trailing slash is dropped.
func ParseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBaseURL, err)
	}

	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%w: scheme must be http or https", ErrInvalidBaseURL)
	case u.Hostname() == "":
		return nil, fmt.Errorf("%w: host is required", ErrInvalidBaseURL)
	case u.User != nil:
		return nil, fmt.Errorf("%w: credentials are not allowed", ErrInvalidBaseURL)
	case u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("%w: query and fragment are not allowed", ErrInvalidBaseURL)
	}

	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""

	return u, nil
}

// Options configure an Importer.
type Options struct {
	// BaseURL is the source of the exports, DefaultBaseURL when empty. An
	// air-gapped mirror serves "<BaseURL>/<Ecosystem>/all.zip".
	BaseURL string
	// Ecosystems are the OSV ecosystems whose affected packages are kept.
	Ecosystems []string
	// TempDir holds the downloaded archive; the system default when empty.
	TempDir string
	// MaxArchiveBytes caps one download; DefaultMaxArchiveBytes when zero.
	MaxArchiveBytes int64
	// Client overrides the HTTP client (tests).
	Client *http.Client
}

// Store is the part of the mirror database an import writes to; it is
// implemented by database.OSVMirror.
type Store interface {
	// ApplyOSVAdvisories stores the new and modified advisories and returns
	// the packages whose advisories changed.
	ApplyOSVAdvisories(ctx context.Context, advisories []models.OSVAdvisory) ([]models.OSVPackageKey, error)
	// ListOSVAdvisoryIDs returns the ids of the advisories stored for an ecosystem.
	ListOSVAdvisoryIDs(ctx context.Context, ecosystem string) ([]string, error)
	// RemoveOSVAdvisories drops the given advisories from an ecosystem and
	// returns the packages that lost one.
	RemoveOSVAdvisories(ctx context.Context, ecosystem string, ids []string) ([]models.OSVPackageKey, error)
}

// Importer downloads OSV exports and applies them to the mirror.
type Importer struct {
	store  Store
	base   *url.URL
	client *http.Client
	opts   Options
	wanted map[string]bool
	log    *logger.Logger
	// maxDecompressed bounds the decompressed records of one archive.
	maxDecompressed int64
}

// NewImporter validates the options and creates an Importer. The client
// connects to the configured host even when it is on a private network (an
// air-gapped mirror is), honours the proxy environment, and only follows
// redirects that stay on https.
func NewImporter(store Store, opts Options, log *logger.Logger) (*Importer, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}

	if opts.MaxArchiveBytes <= 0 {
		opts.MaxArchiveBytes = DefaultMaxArchiveBytes
	}

	base, err := ParseBaseURL(opts.BaseURL)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]bool, len(opts.Ecosystems))

	for _, e := range opts.Ecosystems {
		if !Supported(e) {
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedEcosystem, e)
		}

		wanted[e] = true
	}

	client := opts.Client
	if client == nil {
		client = telemetry.WrapClient(netutil.NewDownloadClient(downloadTimeout, dialHost(base)))
	}

	budget := int64(math.MaxInt64)
	if opts.MaxArchiveBytes <= math.MaxInt64/maxDecompressRatio {
		budget = opts.MaxArchiveBytes * maxDecompressRatio
	}

	return &Importer{store: store, base: base, client: client, opts: opts, wanted: wanted, log: log, maxDecompressed: budget}, nil
}

// dialHost is the host:port of u with the scheme's default port.
func dialHost(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}

	if u.Scheme == "http" {
		return u.Host + ":80"
	}

	return u.Host + ":443"
}

// Result is the outcome of importing one ecosystem.
type Result struct {
	Ecosystem string
	// NotModified is set when the source reported the archive unchanged.
	NotModified bool
	// Baseline is set for the first import of the ecosystem.
	Baseline bool
	// Records counts the records applied or checked against the stored copy.
	Records int
	// Skipped counts malformed records.
	Skipped int
	// Removed counts the advisories the export no longer lists, dropped from
	// the ecosystem.
	Removed int
	// Bytes is the size of the downloaded archive.
	Bytes int64
	// Touched are the packages whose advisories changed.
	Touched map[models.OSVPackageKey]struct{}
}

// ArchiveURL is where the export of an ecosystem is fetched from.
func (i *Importer) ArchiveURL(ecosystem string) string {
	return i.base.String() + "/" + url.PathEscape(ecosystem) + "/all.zip"
}

// Import downloads the export of ecosystem when it changed and applies the
// records modified since the high-water mark of st. It updates st (validators,
// high-water mark, timestamps, error) but does not save it: the caller saves
// it once the result is accepted. A failure leaves the high-water mark where it
// was, so the next run retries the same records.
func (i *Importer) Import(ctx context.Context, ecosystem string, st *models.OSVMirrorState) (*Result, error) {
	start := time.Now()
	res := &Result{Ecosystem: ecosystem, Baseline: st.HighWater == nil, Touched: map[models.OSVPackageKey]struct{}{}}
	archive := i.ArchiveURL(ecosystem)

	checked := start.UTC()
	st.LastCheckedAt = &checked
	st.SourceURL = archive

	file, header, err := i.download(ctx, archive, st)
	if err != nil {
		return res, err
	}

	if file == nil {
		res.NotModified = true

		return res, nil
	}

	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()

	res.Bytes = header.size

	// The archive lists every advisory of the ecosystem, so one that is stored
	// and not listed any more was deleted at the source. Every record read
	// below takes its id off this set; what is left is removed at the end.
	stale, err := i.storedIDs(ctx, ecosystem)
	if err != nil {
		return res, err
	}

	highWater, listed, err := i.applyArchive(ctx, file.Name(), st.HighWater, res, stale)
	if err != nil {
		return res, err
	}

	// An archive without any record is not a statement that all are gone.
	if listed > 0 {
		if err := i.removeStale(ctx, ecosystem, stale, res); err != nil {
			return res, err
		}
	}

	st.HighWater = highWater
	st.ETag = header.etag
	st.ArchiveModified = header.lastModified
	st.LastError = ""
	imported := time.Now().UTC()
	st.LastImportedAt = &imported
	st.LastImportSeconds = time.Since(start).Seconds()

	return res, nil
}

type archiveHeader struct {
	etag, lastModified string
	size               int64
}

// download fetches the archive to a temporary file. It returns a nil file when
// the source answered 304 Not Modified to the stored validators.
func (i *Importer) download(ctx context.Context, archive string, st *models.OSVMirrorState) (*os.File, archiveHeader, error) {
	var h archiveHeader

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archive, nil)
	if err != nil {
		return nil, h, fmt.Errorf("build osv request: %w", err)
	}

	req.Header.Set("User-Agent", "openartifact-osv-mirror")

	if st.ETag != "" {
		req.Header.Set("If-None-Match", st.ETag)
	} else if st.ArchiveModified != "" {
		req.Header.Set("If-Modified-Since", st.ArchiveModified)
	}

	resp, err := i.client.Do(req)
	if err != nil {
		return nil, h, fmt.Errorf("download osv archive: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, h, nil
	case http.StatusOK:
	default:
		return nil, h, fmt.Errorf("%w: %s", ErrBadStatus, resp.Status)
	}

	if resp.ContentLength > i.opts.MaxArchiveBytes {
		return nil, h, ErrArchiveTooLarge
	}

	file, err := os.CreateTemp(i.opts.TempDir, "osv-*.zip")
	if err != nil {
		return nil, h, fmt.Errorf("create osv temp file: %w", err)
	}

	// Google Cloud Storage sends the CRC32C of the stored object. A decoded
	// body (the transport undid a gzip) is not the stored object.
	var want string
	if !resp.Uncompressed {
		want = googHashes(resp.Header)["crc32c"]
	}

	sum := crc32.New(crc32.MakeTable(crc32.Castagnoli))

	n, err := io.Copy(io.MultiWriter(file, sum), io.LimitReader(resp.Body, i.opts.MaxArchiveBytes+1))
	if err == nil && n > i.opts.MaxArchiveBytes {
		err = ErrArchiveTooLarge
	}

	if err == nil && want != "" && want != base64.StdEncoding.EncodeToString(sum.Sum(nil)) {
		err = ErrChecksumMismatch
	}

	if err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())

		return nil, h, fmt.Errorf("download osv archive: %w", err)
	}

	return file, archiveHeader{etag: resp.Header.Get("ETag"), lastModified: resp.Header.Get("Last-Modified"), size: n}, nil
}

// googHashes parses the X-Goog-Hash headers, "crc32c=<base64>" and
// "md5=<base64>" in separate headers or one comma-separated list, into
// algorithm → base64 digest. It is an integrity check of the transfer: the
// digest comes from the same server as the data, it authenticates nothing.
func googHashes(h http.Header) map[string]string {
	out := map[string]string{}

	for _, v := range h.Values("X-Goog-Hash") {
		for part := range strings.SplitSeq(v, ",") {
			if alg, digest, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
				out[alg] = digest
			}
		}
	}

	return out
}

// storedIDs returns the ids of the advisories stored for an ecosystem as a set.
func (i *Importer) storedIDs(ctx context.Context, ecosystem string) (map[string]struct{}, error) {
	ids, err := i.store.ListOSVAdvisoryIDs(ctx, ecosystem)
	if err != nil {
		return nil, fmt.Errorf("list stored osv advisories: %w", err)
	}

	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}

	return set, nil
}

// removeStale drops the advisories left in stale, in batches, and queues the
// packages that lost one for a re-match.
func (i *Importer) removeStale(ctx context.Context, ecosystem string, stale map[string]struct{}, res *Result) error {
	for batch := range slices.Chunk(slices.Sorted(maps.Keys(stale)), batchSize) {
		keys, err := i.store.RemoveOSVAdvisories(ctx, ecosystem, batch)
		if err != nil {
			return fmt.Errorf("remove osv advisories: %w", err)
		}

		for _, k := range keys {
			res.Touched[k] = struct{}{}
		}

		res.Removed += len(batch)
	}

	return nil
}

// applyArchive reads the records of the archive modified at or after since and
// stores them in batches. It takes the id of every record it meets, parsed or
// not, off stale, and returns the new high-water mark and the number of
// records the archive lists.
func (i *Importer) applyArchive(
	ctx context.Context, archive string, since *time.Time, res *Result, stale map[string]struct{},
) (*time.Time, int, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return nil, 0, fmt.Errorf("open osv archive: %w", err)
	}
	defer zr.Close()

	highWater := since
	batch := make([]models.OSVAdvisory, 0, batchSize)
	read := 0

	var decompressed int64

	flush := func() error {
		keys, err := i.store.ApplyOSVAdvisories(ctx, batch)
		if err != nil {
			return fmt.Errorf("store osv advisories: %w", err)
		}

		for _, k := range keys {
			res.Touched[k] = struct{}{}
		}

		batch = batch[:0]

		return nil
	}

	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".json") {
			continue
		}

		if read++; read > maxRecords {
			return nil, 0, fmt.Errorf("%w: more than %d records", ErrArchiveTooLarge, maxRecords)
		}

		if err := ctx.Err(); err != nil {
			return nil, 0, fmt.Errorf("osv import interrupted: %w", err)
		}

		adv, size, err := i.readRecord(f)

		if decompressed += size; decompressed > i.maxDecompressed {
			return nil, 0, fmt.Errorf("%w: more than %d bytes decompressed", ErrArchiveTooLarge, i.maxDecompressed)
		}

		if err != nil {
			// The id of a record that does not parse is its file name, which OSV
			// sets to "<id>.json": it must not count as deleted.
			delete(stale, strings.TrimSuffix(path.Base(f.Name), ".json"))

			if res.Skipped++; res.Skipped <= maxLoggedSkips {
				i.log.Warnw("skipping malformed osv record", "file", truncate(f.Name, 80), "error", truncate(err.Error(), 200))
			}

			continue
		}

		delete(stale, adv.ID)

		if since != nil && adv.Modified.Before(*since) {
			continue
		}

		res.Records++

		if highWater == nil || adv.Modified.After(*highWater) {
			m := adv.Modified
			highWater = &m
		}

		if batch = append(batch, *adv); len(batch) >= batchSize {
			if err := flush(); err != nil {
				return nil, 0, err
			}
		}
	}

	if len(batch) > 0 {
		if err := flush(); err != nil {
			return nil, 0, err
		}
	}

	return highWater, read, nil
}

// readRecord decodes one archive entry. It also returns how many decompressed
// bytes it read, whether or not the record parsed, so the caller can bound the
// total.
func (i *Importer) readRecord(f *zip.File) (*models.OSVAdvisory, int64, error) {
	if f.UncompressedSize64 > maxRecordBytes {
		return nil, 0, ErrArchiveTooLarge
	}

	rc, err := f.Open()
	if err != nil {
		return nil, 0, fmt.Errorf("open archive entry: %w", err)
	}
	defer rc.Close()

	raw, err := io.ReadAll(io.LimitReader(rc, maxRecordBytes+1))
	if err != nil {
		return nil, int64(len(raw)), fmt.Errorf("read archive entry: %w", err)
	}

	if len(raw) > maxRecordBytes {
		return nil, int64(len(raw)), ErrArchiveTooLarge
	}

	adv, err := ParseRecord(raw, i.wanted)

	return adv, int64(len(raw)), err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}
