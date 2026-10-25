// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// azureAPIVersion is the x-ms-version of every request and the sv of every SAS.
const azureAPIVersion = "2020-10-02"

// Sentinel errors for Azure Blob Store.
var (
	ErrAzureAccountNameRequired        = errors.New("azure: AzureAccountName is required")
	ErrAzureAccountKeyRequired         = errors.New("azure: AzureAccountKey is required")
	ErrAzureAccountKeyInvalid          = errors.New("azure: AzureAccountKey is not valid base64")
	ErrAzureContainerNameRequired      = errors.New("azure: AzureContainerName is required")
	ErrAzureEndpointInvalid            = errors.New("azure: AzureEndpoint must be an http(s) URL without query")
	ErrAzureMSDateHeaderRequired       = errors.New("azure: x-ms-date header is required")
	ErrAzurePutMetadataSHA256Required  = errors.New("AzureBlobStore.Put requires \"sha256\" in metadata")
	ErrAzureMissingContentLengthHeader = errors.New("azure: missing Content-Length header")
	ErrAzureUnexpectedStatus           = errors.New("azure: unexpected response")
)

// AzureBlobStore implements BlobStore for Azure Blob Storage using the REST API.
type AzureBlobStore struct {
	accountName   string
	accountKey    []byte // decoded
	containerName string
	baseURL       string // <endpoint>/<container>, no trailing slash
	presignTTL    time.Duration
	client        *http.Client

	// now is the clock of request dates and SAS expiries.
	now func() time.Time
	// copyPollInterval is the first wait between polls of a pending copy.
	copyPollInterval time.Duration
}

// NewAzureBlobStore creates a new Azure Blob Storage backend. The blob service
// is cfg.AzureEndpoint, or https://<account>.blob.core.windows.net when empty.
func NewAzureBlobStore(cfg StorageConfig) (*AzureBlobStore, error) {
	if cfg.AzureAccountName == "" {
		return nil, ErrAzureAccountNameRequired
	}

	if cfg.AzureAccountKey == "" {
		return nil, ErrAzureAccountKeyRequired
	}

	if cfg.AzureContainerName == "" {
		return nil, ErrAzureContainerNameRequired
	}

	key, err := base64.StdEncoding.DecodeString(cfg.AzureAccountKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAzureAccountKeyInvalid, err)
	}

	endpoint := strings.TrimRight(strings.TrimSpace(cfg.AzureEndpoint), "/")
	if endpoint == "" {
		endpoint = "https://" + cfg.AzureAccountName + ".blob.core.windows.net"
	}

	if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: %q", ErrAzureEndpointInvalid, cfg.AzureEndpoint)
	}

	ttl := cfg.PresignTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}

	return &AzureBlobStore{
		accountName:      cfg.AzureAccountName,
		accountKey:       key,
		containerName:    cfg.AzureContainerName,
		baseURL:          endpoint + "/" + cfg.AzureContainerName,
		presignTTL:       ttl,
		client:           newStreamingHTTPClient(nil),
		now:              time.Now,
		copyPollInterval: azureCopyPollInterval,
	}, nil
}

// blobKey computes the blob key for a given SHA-256 hex string.
func azureBlobKey(sha256 string) string {
	return "blobs/" + sha256[:2] + "/" + sha256[2:4] + "/" + sha256
}

// azureCall describes one request to the Blob service.
type azureCall struct {
	method string
	// blob is the blob name inside the container; empty addresses the container.
	blob  string
	query url.Values
	// header carries the x-ms-*, conditional and content headers. x-ms-date,
	// x-ms-version and Authorization are added by newRequest.
	header map[string]string
	body   io.Reader
	// length is the body size in bytes; a zero length sends no body at all.
	length int64
}

// blobURL returns the URL of a blob (or of the container for an empty name).
func (a *AzureBlobStore) blobURL(blob string) string {
	if blob == "" {
		return a.baseURL
	}

	return a.baseURL + "/" + escapeBlobName(blob)
}

// escapeBlobName percent-encodes each segment of a blob name.
func escapeBlobName(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}

	return strings.Join(parts, "/")
}

// newRequest builds and Shared Key signs a request.
func (a *AzureBlobStore) newRequest(ctx context.Context, c azureCall) (*http.Request, error) {
	target := a.blobURL(c.blob)
	if len(c.query) > 0 {
		target += "?" + c.query.Encode()
	}

	body := c.body
	if body != nil && c.length == 0 {
		// A zero-length *os.File body would be sent chunked (unknown length),
		// which Azure rejects; http.NoBody sends Content-Length: 0.
		body = http.NoBody
	}

	req, err := http.NewRequestWithContext(ctx, c.method, target, body)
	if err != nil {
		return nil, fmt.Errorf("azure: create request: %w", err)
	}

	if body != nil {
		req.ContentLength = c.length
	}

	for k, v := range c.header {
		req.Header.Set(k, v)
	}

	req.Header.Set("X-Ms-Date", a.now().UTC().Format(http.TimeFormat))
	req.Header.Set("X-Ms-Version", azureAPIVersion)

	if err := a.signRequest(req); err != nil {
		return nil, err
	}

	return req, nil
}

// do sends one request. The caller closes the response body.
func (a *AzureBlobStore) do(ctx context.Context, c azureCall) (*http.Response, error) {
	req, err := a.newRequest(ctx, c)
	if err != nil {
		return nil, err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure: %s request: %w", strings.ToLower(c.method), err)
	}

	return resp, nil
}

// azureOK reports a 2xx response.
func azureOK(resp *http.Response) bool {
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// azureStatusError describes an unexpected response; the start of the body
// carries Azure's error code.
func azureStatusError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	return fmt.Errorf("%w: %s returned %d: %s", ErrAzureUnexpectedStatus, op, resp.StatusCode, strings.TrimSpace(string(body)))
}

// signRequest adds the Shared Key Authorization header to req. Every x-ms-*
// header of req is signed, so none can be added later without invalidating it.
func (a *AzureBlobStore) signRequest(req *http.Request) error {
	stringToSign, err := a.sharedKeyStringToSign(req)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", fmt.Sprintf("SharedKey %s:%s", a.accountName, a.sign(stringToSign)))

	return nil
}

// sign returns the base64 HMAC-SHA256 of s under the account key.
func (a *AzureBlobStore) sign(s string) string {
	h := hmac.New(sha256.New, a.accountKey)
	h.Write([]byte(s))

	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// sharedKeyStringToSign renders the Shared Key string to sign of req for the
// Blob service (x-ms-version >= 2015-02-21): the verb, the standard headers in
// their fixed positions, the canonicalized x-ms-* headers and the
// canonicalized resource. The Date position stays empty because x-ms-date is
// signed instead.
func (a *AzureBlobStore) sharedKeyStringToSign(req *http.Request) (string, error) {
	if req.Header.Get("X-Ms-Date") == "" {
		return "", ErrAzureMSDateHeaderRequired
	}

	// Azure (x-ms-version >= 2015-02-21) wants an empty Content-Length string
	// to sign when the length is zero.
	length := ""
	if req.ContentLength > 0 {
		length = strconv.FormatInt(req.ContentLength, 10)
	}

	h := req.Header.Get

	return strings.Join([]string{
		req.Method,
		h("Content-Encoding"),
		h("Content-Language"),
		length,
		h("Content-Md5"),
		h("Content-Type"),
		"", // Date
		h("If-Modified-Since"),
		h("If-Match"),
		h("If-None-Match"),
		h("If-Unmodified-Since"),
		h("Range"),
		canonicalizedMSHeaders(req.Header) + a.canonicalizedResource(req.URL),
	}, "\n"), nil
}

// canonicalizedMSHeaders renders the sorted, lowercased x-ms-* headers as
// "name:value\n" lines for the Shared Key string to sign.
func canonicalizedMSHeaders(headers http.Header) string {
	names := make([]string, 0, len(headers))
	values := make(map[string]string, len(headers))

	for k, v := range headers {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-ms-") {
			names = append(names, lk)
			values[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}

	sort.Strings(names)

	var sb strings.Builder
	for _, n := range names {
		sb.WriteString(n + ":" + values[n] + "\n")
	}

	return sb.String()
}

// canonicalizedResource renders the Shared Key canonicalized resource of u:
// "/<account>" plus the URL path as sent, then the lowercased, sorted query
// parameters as "\nname:value" lines (several values sorted and comma-joined).
// The path is the request path, so with a path-style endpoint such as Azurite
// (http://host:10000/devstoreaccount1/<container>) the account appears twice.
func (a *AzureBlobStore) canonicalizedResource(u *url.URL) string {
	var sb strings.Builder

	sb.WriteString("/" + a.accountName + u.EscapedPath())

	params := map[string][]string{}

	for name, values := range u.Query() {
		lname := strings.ToLower(name)
		params[lname] = append(params[lname], values...)
	}

	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		values := params[name]
		sort.Strings(values)
		sb.WriteString("\n" + name + ":" + strings.Join(values, ","))
	}

	return sb.String()
}

// azureMetaName maps an arbitrary metadata key to a valid Azure metadata name
// (a C# identifier: letters, digits, underscore; not starting with a digit).
func azureMetaName(key string) string {
	b := []byte(strings.ToLower(key))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			b[i] = '_'
		}
	}

	if len(b) == 0 || (b[0] >= '0' && b[0] <= '9') {
		return "_" + string(b)
	}

	return string(b)
}

// azureMetaHeaders renders blob metadata as x-ms-meta-* request headers. The
// sha256 entry is the blob's name and is not stored.
func azureMetaHeaders(metadata map[string]string) map[string]string {
	out := make(map[string]string, len(metadata))

	for key, value := range metadata {
		if key != "sha256" {
			out["x-ms-meta-"+azureMetaName(key)] = value
		}
	}

	return out
}

// Put stores a blob. The metadata map must contain a "sha256" key.
func (a *AzureBlobStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	sha, ok := metadata["sha256"]
	if !ok {
		return "", ErrAzurePutMetadataSHA256Required
	}

	if err := ValidateBlobID(sha); err != nil {
		return "", err
	}

	// Spool to a private temp file: it yields the content length without
	// buffering in memory and proves the bytes hash to the claimed digest
	// before anything becomes visible.
	spool, size, err := spoolVerified(data, sha, "", "azure-put-*.tmp")
	if err != nil {
		return "", fmt.Errorf("azure: %w", err)
	}

	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()

	// Verified content already present is never overwritten.
	exists, err := a.Exists(ctx, sha)
	if err != nil {
		return "", err
	}

	if exists {
		return sha, nil
	}

	headers := map[string]string{
		"x-ms-blob-type": "BlockBlob",
		"Content-Type":   "application/octet-stream",
	}
	maps.Copy(headers, azureMetaHeaders(metadata))

	resp, err := a.do(ctx, azureCall{
		method: http.MethodPut,
		blob:   azureBlobKey(sha),
		header: headers,
		body:   spool,
		length: size,
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if !azureOK(resp) {
		return "", azureStatusError("put", resp)
	}

	return sha, nil
}

// Get retrieves a blob by its SHA-256 identifier.
func (a *AzureBlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	resp, err := a.do(ctx, azureCall{method: http.MethodGet, blob: azureBlobKey(id)})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
	}

	if !azureOK(resp) {
		defer func() { _ = resp.Body.Close() }()

		return nil, azureStatusError("get", resp)
	}

	return resp.Body, nil
}

// GetFrom implements RangeReader with a ranged Get Blob (x-ms-range).
func (a *AzureBlobStore) GetFrom(ctx context.Context, id string, offset int64) (io.ReadCloser, error) {
	if offset == 0 {
		return a.Get(ctx, id)
	}

	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	if offset < 0 {
		return nil, ErrOffsetOutOfRange
	}

	resp, err := a.do(ctx, azureCall{method: http.MethodGet, blob: azureBlobKey(id), header: map[string]string{"x-ms-range": rangeHeader(offset)}})
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusNotFound:
		_ = resp.Body.Close()

		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
	case http.StatusRequestedRangeNotSatisfiable:
		_ = resp.Body.Close()

		return nil, fmt.Errorf("%w: %d", ErrOffsetOutOfRange, offset)
	case http.StatusPartialContent:
		return resp.Body, nil
	case http.StatusOK:
		// The service ignored the range: skip the prefix ourselves.
		return skipTo(resp.Body, offset)
	}

	defer func() { _ = resp.Body.Close() }()

	return nil, azureStatusError("get", resp)
}

// Delete removes a blob by its SHA-256 identifier.
func (a *AzureBlobStore) Delete(ctx context.Context, id string) error {
	if err := ValidateBlobID(id); err != nil {
		return err
	}

	return a.deleteBlob(ctx, azureBlobKey(id))
}

// deleteBlob removes the blob name; a missing blob is not an error.
func (a *AzureBlobStore) deleteBlob(ctx context.Context, blob string) error {
	resp, err := a.do(ctx, azureCall{method: http.MethodDelete, blob: blob})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound || azureOK(resp) {
		return nil
	}

	return azureStatusError("delete", resp)
}

// headBlob returns the response headers of a HEAD of the blob name, or an
// error wrapping ErrBlobNotFound.
func (a *AzureBlobStore) headBlob(ctx context.Context, blob string) (http.Header, error) {
	resp, err := a.do(ctx, azureCall{method: http.MethodHead, blob: blob})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, blob)
	}

	if !azureOK(resp) {
		return nil, azureStatusError("head", resp)
	}

	return resp.Header, nil
}

// azureContentLength reads the blob size from HEAD response headers.
func azureContentLength(h http.Header) (int64, error) {
	raw := h.Get("Content-Length")
	if raw == "" {
		return 0, ErrAzureMissingContentLengthHeader
	}

	size, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("azure: parse Content-Length: %w", err)
	}

	return size, nil
}

// Exists checks whether a blob exists using HEAD.
func (a *AzureBlobStore) Exists(ctx context.Context, id string) (bool, error) {
	if err := ValidateBlobID(id); err != nil {
		return false, err
	}

	if _, err := a.headBlob(ctx, azureBlobKey(id)); err != nil {
		if errors.Is(err, ErrBlobNotFound) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// GetSize returns the size of a blob in bytes using HEAD.
func (a *AzureBlobStore) GetSize(ctx context.Context, id string) (int64, error) {
	if err := ValidateBlobID(id); err != nil {
		return 0, err
	}

	h, err := a.headBlob(ctx, azureBlobKey(id))
	if err != nil {
		return 0, err
	}

	return azureContentLength(h)
}

// GetMetadata retrieves metadata for a blob using HEAD.
func (a *AzureBlobStore) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	h, err := a.headBlob(ctx, azureBlobKey(id))
	if err != nil {
		if errors.Is(err, ErrBlobNotFound) {
			return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
		}

		return nil, err
	}

	// Extract metadata from x-ms-meta-* headers
	metadata := make(map[string]string)

	for key, values := range h {
		if after, ok := strings.CutPrefix(key, "X-Ms-Meta-"); ok {
			metaKey := after
			if len(values) > 0 {
				metadata[metaKey] = values[0]
			}
		}
	}

	return metadata, nil
}

// azureListResult is the subset of the List Blobs response the store needs.
type azureListResult struct {
	Blobs []struct {
		Name       string `xml:"Name"`
		Properties struct {
			LastModified string `xml:"Last-Modified"`
		} `xml:"Properties"`
	} `xml:"Blobs>Blob"`
	NextMarker string `xml:"NextMarker"`
}

// listBlobs fetches one page of List Blobs under prefix.
func (a *AzureBlobStore) listBlobs(ctx context.Context, prefix, marker string, limit int) (azureListResult, error) {
	query := url.Values{
		"comp":       {"list"},
		"restype":    {"container"},
		"prefix":     {prefix},
		"maxresults": {strconv.Itoa(limit)},
	}
	if marker != "" {
		query.Set("marker", marker)
	}

	resp, err := a.do(ctx, azureCall{method: http.MethodGet, query: query})
	if err != nil {
		return azureListResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return azureListResult{}, azureStatusError("list", resp)
	}

	var res azureListResult
	if err := xml.NewDecoder(resp.Body).Decode(&res); err != nil {
		return azureListResult{}, fmt.Errorf("azure: decode list response: %w", err)
	}

	return res, nil
}

// List returns one page of at most limit blob ids after token, an Azure
// NextMarker. Blobs are returned in name order, which for the ab/cd/<sha>
// layout is ascending sha order. Entries that are not blob ids are skipped.
func (a *AzureBlobStore) List(ctx context.Context, token string, limit int) ([]string, string, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}

	res, err := a.listBlobs(ctx, "blobs/", token, limit)
	if err != nil {
		return nil, "", err
	}

	ids := make([]string, 0, len(res.Blobs))

	for _, b := range res.Blobs {
		if id := path.Base(b.Name); ValidateBlobID(id) == nil {
			ids = append(ids, id)
		}
	}

	return ids, res.NextMarker, nil
}

// ExistsBySHA256 checks if a blob with the given SHA-256 hash exists.
func (a *AzureBlobStore) ExistsBySHA256(ctx context.Context, sha256 string) (string, bool, error) {
	if err := ValidateBlobID(sha256); err != nil {
		return "", false, err
	}

	exists, err := a.Exists(ctx, sha256)
	if err != nil {
		return "", false, err
	}

	if exists {
		return sha256, true, nil
	}

	return "", false, nil
}

// HealthCheck verifies that the Azure container is accessible.
func (a *AzureBlobStore) HealthCheck(ctx context.Context) error {
	resp, err := a.do(ctx, azureCall{method: http.MethodGet, query: url.Values{"restype": {"container"}}})
	if err != nil {
		return fmt.Errorf("azure: health check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !azureOK(resp) {
		return fmt.Errorf("azure: health check: %w", azureStatusError("container check", resp))
	}

	return nil
}
