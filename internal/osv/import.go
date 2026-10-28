// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package osv

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
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

// Importer downloads OSV exports and applies them to the mirror.
type Importer struct {
	store  database.OSVStore
	base   *url.URL
	client *http.Client
	opts   Options
	wanted map[string]bool
	log    *logger.Logger
}

// NewImporter validates the options and creates an Importer. The client
// connects to the configured host even when it is on a private network (an
// air-gapped mirror is), honours the proxy environment, and only follows
// redirects that stay on https.
func NewImporter(store database.OSVStore, opts Options, log *logger.Logger) (*Importer, error) {
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

	return &Importer{store: store, base: base, client: client, opts: opts, wanted: wanted, log: log}, nil
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

	highWater, err := i.applyArchive(ctx, file.Name(), st.HighWater, res)
	if err != nil {
		return res, err
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

	n, err := io.Copy(file, io.LimitReader(resp.Body, i.opts.MaxArchiveBytes+1))
	if err == nil && n > i.opts.MaxArchiveBytes {
		err = ErrArchiveTooLarge
	}

	if err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())

		return nil, h, fmt.Errorf("download osv archive: %w", err)
	}

	return file, archiveHeader{etag: resp.Header.Get("ETag"), lastModified: resp.Header.Get("Last-Modified"), size: n}, nil
}

// applyArchive reads the records of the archive modified at or after since and
// stores them in batches. It returns the new high-water mark.
func (i *Importer) applyArchive(ctx context.Context, path string, since *time.Time, res *Result) (*time.Time, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open osv archive: %w", err)
	}
	defer zr.Close()

	highWater := since
	batch := make([]models.OSVAdvisory, 0, batchSize)
	read := 0

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
			return nil, fmt.Errorf("%w: more than %d records", ErrArchiveTooLarge, maxRecords)
		}

		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("osv import interrupted: %w", err)
		}

		adv, err := i.readRecord(f)
		if err != nil {
			if res.Skipped++; res.Skipped <= maxLoggedSkips {
				i.log.Warnw("skipping malformed osv record", "file", truncate(f.Name, 80), "error", truncate(err.Error(), 200))
			}

			continue
		}

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
				return nil, err
			}
		}
	}

	if len(batch) > 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}

	return highWater, nil
}

func (i *Importer) readRecord(f *zip.File) (*models.OSVAdvisory, error) {
	if f.UncompressedSize64 > maxRecordBytes {
		return nil, ErrArchiveTooLarge
	}

	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open archive entry: %w", err)
	}
	defer rc.Close()

	raw, err := io.ReadAll(io.LimitReader(rc, maxRecordBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read archive entry: %w", err)
	}

	if len(raw) > maxRecordBytes {
		return nil, ErrArchiveTooLarge
	}

	return ParseRecord(raw, i.wanted)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}
