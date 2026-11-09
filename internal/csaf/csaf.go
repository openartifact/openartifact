// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package csaf imports vendor VEX statements from CSAF 2.0 providers: it
// discovers the documents of a provider-metadata.json, a ROLIE feed or a
// single document URL, checks their integrity (hash files, detached OpenPGP
// signatures; CSAF 2.0 section 7) and reads their not_affected and fixed
// product statuses.
package csaf

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/telemetry"
)

const (
	// maxDocumentBytes caps one CSAF document.
	maxDocumentBytes = 32 << 20
	// maxIndexBytes caps a provider metadata, ROLIE feed or changes.csv.
	maxIndexBytes = 64 << 20
	// maxSidecarBytes caps a hash, signature or key file.
	maxSidecarBytes = 1 << 20
	// maxDocuments caps the documents fetched by one run; the oldest changes
	// go first, so the next run continues from the cursor.
	maxDocuments = 5000
	// maxRedirects caps the redirects of a request when the client sets no policy.
	maxRedirects = 10
)

// Source is a CSAF provider to import.
type Source struct {
	// URL is a provider-metadata.json, a ROLIE feed or a single CSAF document.
	URL string
	// RequireSignature rejects documents without a valid OpenPGP signature.
	RequireSignature bool
	// PGPKeys are armored public keys the signatures are checked against;
	// empty uses the provider metadata's public_openpgp_keys whose
	// fingerprint matches.
	PGPKeys string
	// Since skips the documents not changed after it (nil: all).
	Since *time.Time
}

// Document is an imported CSAF document.
type Document struct {
	URL        string
	TrackingID string
	Released   time.Time // current_release_date
	Statements []models.VEXFeedStatement
}

// Rejection is a document that failed its integrity check or could not be
// fetched or read. Reason is safe to show to an administrator.
type Rejection struct {
	URL    string
	Reason string
}

// Result is the outcome of one Fetch.
type Result struct {
	Documents []Document
	Rejected  []Rejection
	// Cursor is the newest change time of the accepted documents (the ROLIE
	// entry's updated, the changes.csv time, else current_release_date), to
	// pass as the next Source.Since; nil when no document was accepted.
	Cursor *time.Time
}

// Fetcher fetches CSAF providers over an SSRF-safe client.
type Fetcher struct {
	client *http.Client
}

// fetchError is a fetch failure whose message is safe to show to an administrator.
type fetchError string

func (e fetchError) Error() string { return string(e) }

const (
	errNotFound      fetchError = "not found"
	errUnreachable   fetchError = "connection failed"
	errTooLarge      fetchError = "response too large"
	errBadURL        fetchError = "invalid URL"
	errDowngrade     fetchError = "refusing to leave https"
	errRedirects     fetchError = "too many redirects"
	errUnknownSource fetchError = "not a CSAF provider metadata, ROLIE feed or document"
	errPinnedKeys    fetchError = "the configured OpenPGP keys cannot be read"
)

// NewFetcher returns a Fetcher using client; nil uses an SSRF-safe client.
// Redirects from https to another scheme are refused whatever the client.
func NewFetcher(client *http.Client) *Fetcher {
	if client == nil {
		client = telemetry.WrapClient(netutil.SafeHTTPClient(60 * time.Second))
	}

	c := *client
	next := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errDowngrade
		}

		if next != nil {
			return next(req, via)
		}

		if len(via) >= maxRedirects {
			return errRedirects
		}

		return nil
	}

	return &Fetcher{client: &c}
}

// index is any of the JSON entry points: a CSAF document, a ROLIE feed or a provider metadata.
type index struct {
	Document          json.RawMessage `json:"document"`
	Feed              *rolieFeed      `json:"feed"`
	MetadataVersion   string          `json:"metadata_version"`
	Distributions     []distribution  `json:"distributions"`
	PublicOpenPGPKeys []pgpKeyRef     `json:"public_openpgp_keys"`
}

type distribution struct {
	DirectoryURL string `json:"directory_url"`
	ROLIE        *struct {
		Feeds []struct {
			URL string `json:"url"`
		} `json:"feeds"`
	} `json:"rolie"`
}

type pgpKeyRef struct {
	Fingerprint string `json:"fingerprint"`
	URL         string `json:"url"`
}

type rolieFeed struct {
	Entry []rolieEntry `json:"entry"`
}

type rolieEntry struct {
	Updated string      `json:"updated"`
	Link    []rolieLink `json:"link"`
	Content struct {
		Src string `json:"src"`
	} `json:"content"`
}

type rolieLink struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
}

// candidate is a document to fetch.
type candidate struct {
	url      string
	changed  time.Time // zero when the listing does not tell
	hashURLs []string  // listed hash files
	sigURL   string    // listed signature; "" probes <url>.asc
	// probeHash looks for <url>.sha512, else <url>.sha256.
	probeHash bool
	// body and parsed are the document when already fetched (a single document source).
	body   []byte
	parsed *Document
}

// run is the state of one Fetch.
type run struct {
	f       *Fetcher
	src     Source
	keys    openpgp.EntityList
	keyRefs []pgpKeyRef
	keyBase *url.URL
	loaded  bool
}

// Fetch imports the documents of src changed after src.Since. A document
// that cannot be fetched, fails its integrity check or cannot be read is
// rejected and the run goes on; a failure of the source itself (the entry
// point, a ROLIE feed or a changes.csv) is an error, whose message is safe to
// show to an administrator.
func (f *Fetcher) Fetch(ctx context.Context, src Source) (*Result, error) {
	base, err := url.Parse(src.URL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, errBadURL
	}

	r := &run{f: f, src: src}

	if strings.TrimSpace(src.PGPKeys) != "" {
		if r.keys, err = openpgp.ReadArmoredKeyRing(strings.NewReader(src.PGPKeys)); err != nil {
			return nil, errPinnedKeys
		}

		r.loaded = true
	}

	body, err := f.get(ctx, src.URL, maxIndexBytes)
	if err != nil {
		return nil, wrap("source", err)
	}

	var idx index
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, wrap("source", errNotJSON)
	}

	var cands []candidate

	switch {
	case idx.Document != nil:
		cands, err = r.single(body)
	case idx.Feed != nil:
		cands = r.rolie(base, idx.Feed)
	case idx.MetadataVersion != "" || idx.Distributions != nil:
		r.keyRefs, r.keyBase = idx.PublicOpenPGPKeys, base
		cands, err = r.provider(ctx, base, idx.Distributions)
	default:
		err = errUnknownSource
	}

	if err != nil {
		return nil, err
	}

	return r.process(ctx, plan(cands))
}

// single is a source URL that is itself a document.
func (r *run) single(body []byte) ([]candidate, error) {
	if len(body) > maxDocumentBytes {
		return nil, wrap("document", errTooLarge)
	}

	doc, err := ParseDocument(body, r.src.URL)
	if err != nil {
		return nil, wrap("document", err)
	}

	if !r.changedSince(doc.Released) {
		return nil, nil
	}

	return []candidate{{url: r.src.URL, changed: doc.Released, probeHash: true, body: body, parsed: doc}}, nil
}

// provider lists the documents of every distribution of a provider metadata.
func (r *run) provider(ctx context.Context, base *url.URL, dists []distribution) ([]candidate, error) {
	var out []candidate

	for _, d := range dists {
		if d.ROLIE != nil {
			for _, feed := range d.ROLIE.Feeds {
				u, err := resolve(base, feed.URL)
				if err != nil {
					return nil, wrap("ROLIE feed", err)
				}

				f, err := r.fetchFeed(ctx, u)
				if err != nil {
					return nil, err
				}

				out = append(out, r.rolie(u, f)...)
			}
		}

		if d.DirectoryURL != "" {
			c, err := r.directory(ctx, base, d.DirectoryURL)
			if err != nil {
				return nil, err
			}

			out = append(out, c...)
		}
	}

	return out, nil
}

func (r *run) fetchFeed(ctx context.Context, u *url.URL) (*rolieFeed, error) {
	body, err := r.f.get(ctx, u.String(), maxIndexBytes)
	if err != nil {
		return nil, wrap("ROLIE feed", err)
	}

	var idx index
	if err := json.Unmarshal(body, &idx); err != nil || idx.Feed == nil {
		return nil, wrap("ROLIE feed", errNotJSON)
	}

	return idx.Feed, nil
}

// rolie lists the entries of a ROLIE feed changed since the cursor.
func (r *run) rolie(base *url.URL, feed *rolieFeed) []candidate {
	var out []candidate

	for _, e := range feed.Entry {
		c := candidate{}
		c.changed, _ = time.Parse(time.RFC3339, e.Updated)

		if !c.changed.IsZero() && !r.changedSince(c.changed) {
			continue
		}

		href := e.Content.Src

		for _, l := range e.Link {
			switch l.Rel {
			case "self":
				href = cmp.Or(href, l.Href)
			case "hash":
				if u, err := resolve(base, l.Href); err == nil {
					c.hashURLs = append(c.hashURLs, u.String())
				}
			case "signature":
				if u, err := resolve(base, l.Href); err == nil {
					c.sigURL = u.String()
				}
			}
		}

		if u, err := resolve(base, href); err == nil && href != "" {
			c.url = u.String()
			out = append(out, c)
		}
	}

	return out
}

// directory lists the documents of a directory distribution from its changes.csv.
func (r *run) directory(ctx context.Context, base *url.URL, dirURL string) ([]candidate, error) {
	if !strings.HasSuffix(dirURL, "/") {
		dirURL += "/"
	}

	dir, err := resolve(base, dirURL)
	if err != nil {
		return nil, wrap("changes.csv", err)
	}

	changes, err := resolve(dir, "changes.csv")
	if err != nil {
		return nil, wrap("changes.csv", err)
	}

	body, err := r.f.get(ctx, changes.String(), maxIndexBytes)
	if err != nil {
		return nil, wrap("changes.csv", err)
	}

	rd := csv.NewReader(bytes.NewReader(body))
	rd.FieldsPerRecord = -1

	var out []candidate

	for {
		rec, err := rd.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}

		if err != nil {
			return nil, wrap("changes.csv", fetchError("not valid CSV"))
		}

		if len(rec) < 2 || !strings.HasSuffix(rec[0], ".json") {
			continue
		}

		changed, err := time.Parse(time.RFC3339, strings.TrimSpace(rec[1]))
		if err != nil || !r.changedSince(changed) {
			continue
		}

		if u, err := resolve(dir, strings.TrimSpace(rec[0])); err == nil {
			out = append(out, candidate{url: u.String(), changed: changed, probeHash: true})
		}
	}
}

func (r *run) changedSince(t time.Time) bool {
	return r.src.Since == nil || t.After(*r.src.Since)
}

// plan merges the candidates listed several times (keeping the newest change),
// orders them oldest change first and keeps the first maxDocuments.
func plan(cands []candidate) []candidate {
	byURL := map[string]int{}
	out := make([]candidate, 0, len(cands))

	for _, c := range cands {
		i, dup := byURL[c.url]
		if !dup {
			byURL[c.url] = len(out)
			out = append(out, c)

			continue
		}

		prev := &out[i]
		if c.changed.After(prev.changed) {
			prev.changed = c.changed
		}

		prev.hashURLs = append(prev.hashURLs, c.hashURLs...)
		prev.sigURL = cmp.Or(prev.sigURL, c.sigURL)
		prev.probeHash = prev.probeHash || c.probeHash
	}

	slices.SortStableFunc(out, func(a, b candidate) int {
		return cmp.Or(a.changed.Compare(b.changed), strings.Compare(a.url, b.url))
	})

	if len(out) > maxDocuments {
		out = out[:maxDocuments]
	}

	return out
}

// process fetches, checks and parses the planned documents.
func (r *run) process(ctx context.Context, cands []candidate) (*Result, error) {
	res := &Result{}

	for i := range cands {
		c := &cands[i]

		doc, err := r.document(ctx, c)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if err != nil {
			res.Rejected = append(res.Rejected, Rejection{URL: c.url, Reason: err.Error()})

			continue
		}

		res.Documents = append(res.Documents, *doc)

		changed := cmp.Or(c.changed, doc.Released)
		if res.Cursor == nil || changed.After(*res.Cursor) {
			res.Cursor = &changed
		}
	}

	return res, nil
}

// document fetches one document, checks its integrity and parses it. The
// error's message is the client-safe rejection reason.
func (r *run) document(ctx context.Context, c *candidate) (*Document, error) {
	body := c.body
	if body == nil {
		var err error
		if body, err = r.f.get(ctx, c.url, maxDocumentBytes); err != nil {
			return nil, wrap("document", err)
		}
	}

	if err := r.checkHashes(ctx, c, body); err != nil {
		return nil, err
	}

	if err := r.checkSignature(ctx, c, body); err != nil {
		return nil, err
	}

	if c.parsed != nil {
		return c.parsed, nil
	}

	return ParseDocument(body, c.url)
}

// get fetches u with a body cap.
func (f *Fetcher) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errBadURL
	}

	req.Header.Set("User-Agent", "openartifact-csaf")

	resp, err := f.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if fe, ok := errors.AsType[fetchError](err); ok {
			return nil, fe
		}

		return nil, errUnreachable
	}

	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, errNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fetchError("HTTP " + strconv.Itoa(resp.StatusCode))
	case resp.ContentLength > limit:
		return nil, errTooLarge
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		return nil, errUnreachable
	}

	if int64(len(body)) > limit {
		return nil, errTooLarge
	}

	return body, nil
}

// resolve resolves ref against base, refusing other schemes than http(s) and
// leaving https for http.
func resolve(base *url.URL, ref string) (*url.URL, error) {
	u, err := base.Parse(strings.TrimSpace(ref))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errBadURL
	}

	if base.Scheme == "https" && u.Scheme != "https" {
		return nil, errDowngrade
	}

	return u, nil
}

// wrap prefixes a client-safe error with what was being fetched; a context
// error is returned as is.
func wrap(what string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	return fetchError(what + ": " + err.Error())
}
