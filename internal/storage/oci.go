// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

// OCIBlobStore implements BlobStore backed by an OCI-compliant container registry.
//
// Blobs are stored as OCI blobs (application/octet-stream) addressed by their
// SHA-256 digest, which maps naturally to the OCI content-addressable model.
// The registry endpoint is configured via StorageConfig.Endpoint and the
// repository namespace via StorageConfig.Bucket (used as the OCI repository prefix).
//
// Authentication uses HTTP Basic credentials (OCIUsername / OCIPassword).
// Set OCIInsecure=true to allow plain HTTP (development only).
//
// OCI Distribution Spec v1.1 endpoints used:
//   POST   /v2/<repo>/blobs/uploads/          – initiate upload session
//   PUT    /v2/<repo>/blobs/uploads/<uuid>?digest=sha256:<hex> – complete upload
//   GET    /v2/<repo>/blobs/sha256:<hex>       – download blob
//   HEAD   /v2/<repo>/blobs/sha256:<hex>       – existence / size check
//   DELETE /v2/<repo>/blobs/sha256:<hex>       – delete blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sentinel errors for OCI Blob Store.
var (
	ErrOCIEndpointRequired      = errors.New("oci: Endpoint (registry URL) is required")
	ErrOCIBucketRequired        = errors.New("oci: Bucket (repository name) is required")
	ErrOCIPutRequiresSHA256     = errors.New("oci: Put requires \"sha256\" in metadata")
	ErrOCIMissingLocationHeader = errors.New("oci: initiate upload: missing Location header")
)

// OCIBlobStore stores blobs in an OCI-compliant registry.
type OCIBlobStore struct {
	// relay is the server-relayed staging area; nil when no public URL is
	// configured. See relay.go.
	*relay

	client   *http.Client
	base     string // registry base URL, e.g. "https://registry.example.com"
	repo     string // OCI repository, e.g. "openartifact/blobs"
	username string
	password string
}

// NewOCIBlobStore creates a new OCIBlobStore from the provided StorageConfig.
// StorageConfig.Endpoint must be the registry base URL (e.g. "https://registry.example.com").
// StorageConfig.Bucket is used as the OCI repository name.
func NewOCIBlobStore(cfg StorageConfig) (*OCIBlobStore, error) {
	if cfg.Endpoint == "" {
		return nil, ErrOCIEndpointRequired
	}

	if cfg.Bucket == "" {
		return nil, ErrOCIBucketRequired
	}

	var transport http.RoundTripper
	if cfg.OCIInsecure {
		transport = insecureTransport()
	}

	stagingDir := cfg.StagingPath
	if stagingDir == "" {
		stagingDir = filepath.Join(os.TempDir(), "openartifact-staging")
	}

	return &OCIBlobStore{
		relay:    newRelay(stagingDir, cfg.PublicURL),
		client:   newStreamingHTTPClient(transport),
		base:     strings.TrimRight(cfg.Endpoint, "/"),
		repo:     cfg.Bucket,
		username: cfg.OCIUsername,
		password: cfg.OCIPassword,
	}, nil
}

// blobURL returns the OCI blob URL for a given SHA-256 hex digest.
func (o *OCIBlobStore) blobURL(sha256hex string) string {
	return fmt.Sprintf("%s/v2/%s/blobs/sha256:%s", o.base, o.repo, sha256hex)
}

// do executes an HTTP request with optional Basic auth.
func (o *OCIBlobStore) do(req *http.Request) (*http.Response, error) {
	// Credentials are sent only to the configured registry host, never to a
	// foreign host named in a Location header.
	if o.username != "" && o.sameHost(req.URL) {
		req.SetBasicAuth(o.username, o.password)
	}

	return o.client.Do(req)
}

// sameHost reports whether u points at the configured registry host.
func (o *OCIBlobStore) sameHost(u *url.URL) bool {
	b, err := url.Parse(o.base)
	if err != nil {
		return false
	}

	return strings.EqualFold(b.Scheme, u.Scheme) && strings.EqualFold(b.Host, u.Host)
}

// Put uploads a blob to the OCI registry using the monolithic upload flow.
// The metadata map must contain a "sha256" key with the hex-encoded SHA-256 digest.
func (o *OCIBlobStore) Put(ctx context.Context, data io.Reader, metadata map[string]string) (string, error) {
	sha, ok := metadata["sha256"]
	if !ok || sha == "" {
		return "", ErrOCIPutRequiresSHA256
	}

	if err := ValidateBlobID(sha); err != nil {
		return "", err
	}

	// Verify locally before talking to the registry, so a mismatch never
	// leaves an upload session behind and the bytes sent are the bytes hashed.
	spool, size, err := spoolVerified(data, sha, "", "oci-put-*.tmp")
	if err != nil {
		return "", fmt.Errorf("oci: %w", err)
	}

	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()

	// Check if blob already exists (avoid re-upload).
	exists, err := o.Exists(ctx, sha)
	if err != nil {
		return "", err
	}

	if exists {
		return sha, nil
	}

	// Step 1: initiate upload session.
	initURL := fmt.Sprintf("%s/v2/%s/blobs/uploads/", o.base, o.repo)

	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("oci: initiate upload: %w", err)
	}

	initResp, err := o.do(initReq)
	if err != nil {
		return "", fmt.Errorf("oci: initiate upload: %w", err)
	}

	defer func() { _ = initResp.Body.Close() }()

	if initResp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(initResp.Body)
		//nolint:err113 // dynamic error with runtime context
		return "", fmt.Errorf("oci: initiate upload: status %d: %s", initResp.StatusCode, body)
	}

	uploadLocation := initResp.Header.Get("Location")
	if uploadLocation == "" {
		return "", ErrOCIMissingLocationHeader
	}

	// Resolve relative Location header.
	if !strings.HasPrefix(uploadLocation, "http") {
		uploadLocation = o.base + uploadLocation
	}

	// Step 2: complete upload with digest.
	putURL, err := url.Parse(uploadLocation)
	if err != nil {
		return "", fmt.Errorf("oci: parse upload location: %w", err)
	}

	q := putURL.Query()
	q.Set("digest", "sha256:"+sha)
	putURL.RawQuery = q.Encode()

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, putURL.String(), spool)
	if err != nil {
		return "", fmt.Errorf("oci: complete upload: %w", err)
	}

	putReq.ContentLength = size
	if size == 0 {
		putReq.Body = http.NoBody
	}

	putReq.Header.Set("Content-Type", "application/octet-stream")

	putResp, err := o.do(putReq)
	if err != nil {
		return "", fmt.Errorf("oci: complete upload: %w", err)
	}
	defer func() { _ = putResp.Body.Close() }()

	if putResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(putResp.Body)
		//nolint:err113 // dynamic error with runtime context
		return "", fmt.Errorf("oci: complete upload: status %d: %s", putResp.StatusCode, body)
	}

	return sha, nil
}

// Get retrieves a blob by its SHA-256 identifier.
func (o *OCIBlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.blobURL(id), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("oci: get %s: %w", id, err)
	}

	resp, err := o.do(req)
	if err != nil {
		return nil, fmt.Errorf("oci: get %s: %w", id, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		//nolint:err113 // dynamic error with runtime context
		return nil, fmt.Errorf("oci: get %s: status %d: %s", id, resp.StatusCode, body)
	}

	return resp.Body, nil
}

// GetFrom implements RangeReader with a Range request; a registry that
// ignores the header answers 200 and the prefix is skipped locally.
func (o *OCIBlobStore) GetFrom(ctx context.Context, id string, offset int64) (io.ReadCloser, error) {
	if offset == 0 {
		return o.Get(ctx, id)
	}

	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	if offset < 0 {
		return nil, ErrOffsetOutOfRange
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.blobURL(id), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("oci: get %s: %w", id, err)
	}

	req.Header.Set("Range", rangeHeader(offset))

	resp, err := o.do(req)
	if err != nil {
		return nil, fmt.Errorf("oci: get %s: %w", id, err)
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
		return skipTo(resp.Body, offset)
	}

	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	//nolint:err113 // dynamic error with runtime context
	return nil, fmt.Errorf("oci: get %s: status %d: %s", id, resp.StatusCode, body)
}

// Delete removes a blob by its SHA-256 identifier.
func (o *OCIBlobStore) Delete(ctx context.Context, id string) error {
	if err := ValidateBlobID(id); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, o.blobURL(id), http.NoBody)
	if err != nil {
		return fmt.Errorf("oci: delete %s: %w", id, err)
	}

	resp, err := o.do(req)
	if err != nil {
		return fmt.Errorf("oci: delete %s: %w", id, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)

	//nolint:err113 // dynamic error with runtime context
	return fmt.Errorf("oci: delete %s: status %d: %s", id, resp.StatusCode, body)
}

// Exists checks whether a blob exists using HEAD.
func (o *OCIBlobStore) Exists(ctx context.Context, id string) (bool, error) {
	if err := ValidateBlobID(id); err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, o.blobURL(id), http.NoBody)
	if err != nil {
		return false, fmt.Errorf("oci: head %s: %w", id, err)
	}

	resp, err := o.do(req)
	if err != nil {
		return false, fmt.Errorf("oci: head %s: %w", id, err)
	}

	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		//nolint:err113 // dynamic error with runtime context
		return false, fmt.Errorf("oci: head %s: unexpected status %d", id, resp.StatusCode)
	}
}

// GetMetadata returns a minimal metadata map derived from the HEAD response.
func (o *OCIBlobStore) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, o.blobURL(id), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("oci: head %s: %w", id, err)
	}

	resp, err := o.do(req)
	if err != nil {
		return nil, fmt.Errorf("oci: head %s: %w", id, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
	}

	if resp.StatusCode != http.StatusOK {
		//nolint:err113 // dynamic error with runtime context
		return nil, fmt.Errorf("oci: head %s: status %d", id, resp.StatusCode)
	}

	meta := map[string]string{
		"sha256":         id,
		"content-type":   resp.Header.Get("Content-Type"),
		"content-length": resp.Header.Get("Content-Length"),
	}

	return meta, nil
}

// List is not supported: OCI registries expose no flat blob listing.
func (o *OCIBlobStore) List(_ context.Context, _ string, _ int) ([]string, string, error) {
	return nil, "", ErrListNotSupported
}

// GetSize returns the size of a blob in bytes using HEAD.
func (o *OCIBlobStore) GetSize(ctx context.Context, id string) (int64, error) {
	if err := ValidateBlobID(id); err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, o.blobURL(id), http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("oci: head %s: %w", id, err)
	}

	resp, err := o.do(req)
	if err != nil {
		return 0, fmt.Errorf("oci: head %s: %w", id, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
	}

	if resp.StatusCode != http.StatusOK {
		//nolint:err113 // dynamic error with runtime context
		return 0, fmt.Errorf("oci: head %s: status %d", id, resp.StatusCode)
	}

	var size int64
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if _, err := fmt.Sscanf(cl, "%d", &size); err != nil {
			return 0, fmt.Errorf("oci: parse Content-Length: %w", err)
		}
	}

	return size, nil
}

// ExistsBySHA256 checks whether a blob with the given SHA-256 hash exists.
// In OCI, blobs are natively addressed by digest, so this is equivalent to Exists.
func (o *OCIBlobStore) ExistsBySHA256(ctx context.Context, sha256hex string) (string, bool, error) {
	if err := ValidateBlobID(sha256hex); err != nil {
		return "", false, err
	}

	exists, err := o.Exists(ctx, sha256hex)
	if err != nil {
		return "", false, err
	}

	if exists {
		return sha256hex, true, nil
	}

	return "", false, nil
}

// PresignURL returns ErrPresignNotSupported; OCI registries do not support presigned URLs.
func (o *OCIBlobStore) PresignURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", ErrPresignNotSupported
}

// HealthCheck verifies registry connectivity by calling GET /v2/.
func (o *OCIBlobStore) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+"/v2/", http.NoBody)
	if err != nil {
		return fmt.Errorf("oci: health check: %w", err)
	}

	resp, err := o.do(req)
	if err != nil {
		return fmt.Errorf("oci: health check: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	// /v2/ returns 200 (authenticated) or 401 (auth required but reachable).
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusUnauthorized {
		//nolint:err113 // dynamic error with runtime context
		return fmt.Errorf("oci: health check: unexpected status %d", resp.StatusCode)
	}

	return nil
}

// insecureTransport returns an http.RoundTripper that skips TLS verification.
// Used only when OCIInsecure=true (development/testing).
func insecureTransport() http.RoundTripper {
	return &http.Transport{ // plain HTTP; TLS not configured
		DialContext:           (&net.Dialer{Timeout: storageDialTimeout}).DialContext,
		ResponseHeaderTimeout: storageResponseHeaderTimeout,
	}
}

var (
	_ StagingStore          = (*OCIBlobStore)(nil)
	_ MultipartStagingStore = (*OCIBlobStore)(nil)
	_ RelayServer           = (*OCIBlobStore)(nil)
)

// PromoteStaging implements StagingStore: the exact checked staging object is
// pushed to the registry through Put, which verifies its SHA-256. A blob the
// registry already has is left untouched.
func (o *OCIBlobStore) PromoteStaging(ctx context.Context, id, sha256hex, version string, metadata map[string]string) error {
	if o.relay == nil {
		return ErrPresignNotSupported
	}

	if err := ValidateBlobID(sha256hex); err != nil {
		return err
	}

	if _, found, err := o.ExistsBySHA256(ctx, sha256hex); err != nil {
		return err
	} else if found {
		return nil
	}

	src, err := o.openVerified(id, version)
	if err != nil {
		return err
	}

	defer func() { _ = src.Close() }()

	meta := make(map[string]string, len(metadata)+1)
	maps.Copy(meta, metadata)

	meta["sha256"] = sha256hex

	_, err = o.Put(ctx, src, meta)

	return err
}
