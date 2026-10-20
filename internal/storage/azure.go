// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors for Azure Blob Store.
var (
	ErrAzureAccountNameRequired        = errors.New("azure: AzureAccountName is required")
	ErrAzureAccountKeyRequired         = errors.New("azure: AzureAccountKey is required")
	ErrAzureContainerNameRequired      = errors.New("azure: AzureContainerName is required")
	ErrAzureMSDateHeaderRequired       = errors.New("azure: x-ms-date header is required")
	ErrAzurePutMetadataSHA256Required  = errors.New("AzureBlobStore.Put requires \"sha256\" in metadata")
	ErrAzureMissingContentLengthHeader = errors.New("azure: missing Content-Length header")
)

// AzureBlobStore implements BlobStore for Azure Blob Storage using the REST API.
type AzureBlobStore struct {
	accountName   string
	accountKey    string
	containerName string
	baseURL       string
	client        *http.Client
}

// NewAzureBlobStore creates a new Azure Blob Storage backend.
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

	baseURL := fmt.Sprintf("https://%s.blob.core.windows.net/%s", cfg.AzureAccountName, cfg.AzureContainerName)

	return &AzureBlobStore{
		accountName:   cfg.AzureAccountName,
		accountKey:    cfg.AzureAccountKey,
		containerName: cfg.AzureContainerName,
		baseURL:       baseURL,
		client:        &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// blobKey computes the blob key for a given SHA-256 hex string.
func azureBlobKey(sha256 string) string {
	return "blobs/" + sha256[:2] + "/" + sha256[2:4] + "/" + sha256
}

// signRequest signs an Azure Blob Storage request using Shared Key authentication.
func (a *AzureBlobStore) signRequest(method, blobPath string, headers map[string]string, contentLength int64) (string, error) {
	// Decode the account key from base64
	decodedKey, err := base64.StdEncoding.DecodeString(a.accountKey)
	if err != nil {
		return "", fmt.Errorf("azure: decode account key: %w", err)
	}

	// Get the date header
	date := headers["x-ms-date"]
	if date == "" {
		return "", ErrAzureMSDateHeaderRequired
	}

	// Build the string to sign
	var stringToSign string

	switch {
	case method == "PUT":
		// For PUT: include content length and type
		contentType := headers["Content-Type"]
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		stringToSign = fmt.Sprintf("%s\n\n\n%d\n\n%s\n\n\n\n\n\n\nx-ms-blob-type:BlockBlob\nx-ms-date:%s\nx-ms-version:2020-10-02\n/%s/%s/%s",
			method, contentLength, contentType, date, a.accountName, a.containerName, blobPath)
	case strings.Contains(blobPath, "?restype=container"):
		// For container health check
		stringToSign = fmt.Sprintf("%s\n\n\n\n\n\n\n\n\n\n\n\nx-ms-date:%s\nx-ms-version:2020-10-02\n/%s/%s\nrestype:container",
			method, date, a.accountName, a.containerName)
	default:
		// For GET/HEAD/DELETE
		stringToSign = fmt.Sprintf("%s\n\n\n\n\n\n\n\n\n\n\n\nx-ms-date:%s\nx-ms-version:2020-10-02\n/%s/%s/%s",
			method, date, a.accountName, a.containerName, blobPath)
	}

	// Sign with HMAC-SHA256
	h := hmac.New(sha256.New, decodedKey)
	h.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))

	return fmt.Sprintf("SharedKey %s:%s", a.accountName, signature), nil
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

	blobPath := azureBlobKey(sha)
	url := fmt.Sprintf("%s/%s", a.baseURL, blobPath)

	// Spool to a temp file to learn the content length without buffering in memory.
	spool, err := os.CreateTemp("", "azure-put-*.tmp")
	if err != nil {
		return "", fmt.Errorf("azure: create spool file: %w", err)
	}

	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()

	size, err := io.Copy(spool, data)
	if err != nil {
		return "", fmt.Errorf("azure: read data: %w", err)
	}

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("azure: rewind spool: %w", err)
	}

	// Prepare headers
	now := time.Now().UTC().Format(time.RFC1123)
	headers := map[string]string{
		"x-ms-date":      now,
		"x-ms-version":   "2020-10-02",
		"x-ms-blob-type": "BlockBlob",
		"Content-Type":   "application/octet-stream",
	}

	// Sign the request
	authHeader, err := a.signRequest("PUT", blobPath, headers, size)
	if err != nil {
		return "", err
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, spool)
	if err != nil {
		return "", fmt.Errorf("azure: create request: %w", err)
	}

	req.ContentLength = size

	// Set headers
	req.Header.Set("Authorization", authHeader)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-date", now)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-version", "2020-10-02")
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", strconv.FormatInt(size, 10))

	// Add custom metadata headers
	for key, value := range metadata {
		if key != "sha256" {
			req.Header.Set("x-ms-meta-"+key, value)
		}
	}

	// Execute request
	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("azure: put request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("azure: put failed with status %d: %s", resp.StatusCode, string(body)) //nolint:err113 // dynamic error with runtime context
	}

	return sha, nil
}

// Get retrieves a blob by its SHA-256 identifier.
func (a *AzureBlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	blobPath := azureBlobKey(id)
	url := fmt.Sprintf("%s/%s", a.baseURL, blobPath)

	// Prepare headers
	now := time.Now().UTC().Format(time.RFC1123)
	headers := map[string]string{
		"x-ms-date":    now,
		"x-ms-version": "2020-10-02",
	}

	// Sign the request
	authHeader, err := a.signRequest("GET", blobPath, headers, 0)
	if err != nil {
		return nil, err
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("azure: create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", authHeader)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-date", now)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-version", "2020-10-02")

	// Execute request
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure: get request: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("blob not found: %s", id) //nolint:err113 // dynamic error with runtime context
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		return nil, fmt.Errorf("azure: get failed with status %d: %s", resp.StatusCode, string(body)) //nolint:err113 // dynamic error with runtime context
	}

	return resp.Body, nil
}

// Delete removes a blob by its SHA-256 identifier.
func (a *AzureBlobStore) Delete(ctx context.Context, id string) error {
	if err := ValidateBlobID(id); err != nil {
		return err
	}

	blobPath := azureBlobKey(id)
	url := fmt.Sprintf("%s/%s", a.baseURL, blobPath)

	// Prepare headers
	now := time.Now().UTC().Format(time.RFC1123)
	headers := map[string]string{
		"x-ms-date":    now,
		"x-ms-version": "2020-10-02",
	}

	// Sign the request
	authHeader, err := a.signRequest("DELETE", blobPath, headers, 0)
	if err != nil {
		return err
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("azure: create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", authHeader)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-date", now)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-version", "2020-10-02")

	// Execute request
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("azure: delete request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 404 is treated as success (blob already deleted)
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("azure: delete failed with status %d: %s", resp.StatusCode, string(body)) //nolint:err113 // dynamic error with runtime context
	}

	return nil
}

// Exists checks whether a blob exists using HEAD.
func (a *AzureBlobStore) Exists(ctx context.Context, id string) (bool, error) {
	if err := ValidateBlobID(id); err != nil {
		return false, err
	}

	blobPath := azureBlobKey(id)
	url := fmt.Sprintf("%s/%s", a.baseURL, blobPath)

	// Prepare headers
	now := time.Now().UTC().Format(time.RFC1123)
	headers := map[string]string{
		"x-ms-date":    now,
		"x-ms-version": "2020-10-02",
	}

	// Sign the request
	authHeader, err := a.signRequest("HEAD", blobPath, headers, 0)
	if err != nil {
		return false, err
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, fmt.Errorf("azure: create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", authHeader)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-date", now)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-version", "2020-10-02")

	// Execute request
	resp, err := a.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("azure: head request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("azure: head failed with status %d", resp.StatusCode) //nolint:err113 // dynamic error with runtime context
	}

	return true, nil
}

// GetSize returns the size of a blob in bytes using HEAD.
func (a *AzureBlobStore) GetSize(ctx context.Context, id string) (int64, error) {
	if err := ValidateBlobID(id); err != nil {
		return 0, err
	}

	blobPath := azureBlobKey(id)
	url := fmt.Sprintf("%s/%s", a.baseURL, blobPath)

	// Prepare headers
	now := time.Now().UTC().Format(time.RFC1123)
	headers := map[string]string{
		"x-ms-date":    now,
		"x-ms-version": "2020-10-02",
	}

	// Sign the request
	authHeader, err := a.signRequest("HEAD", blobPath, headers, 0)
	if err != nil {
		return 0, err
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, fmt.Errorf("azure: create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", authHeader)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-date", now)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-version", "2020-10-02")

	// Execute request
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("azure: head request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("blob not found: %s", id) //nolint:err113 // dynamic error with runtime context
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("azure: head failed with status %d", resp.StatusCode) //nolint:err113 // dynamic error with runtime context
	}

	contentLength := resp.Header.Get("Content-Length")
	if contentLength == "" {
		return 0, ErrAzureMissingContentLengthHeader
	}

	var size int64

	_, err = fmt.Sscanf(contentLength, "%d", &size)
	if err != nil {
		return 0, fmt.Errorf("azure: parse Content-Length: %w", err)
	}

	return size, nil
}

// GetMetadata retrieves metadata for a blob using HEAD.
func (a *AzureBlobStore) GetMetadata(ctx context.Context, id string) (map[string]string, error) {
	if err := ValidateBlobID(id); err != nil {
		return nil, err
	}

	blobPath := azureBlobKey(id)
	url := fmt.Sprintf("%s/%s", a.baseURL, blobPath)

	// Prepare headers
	now := time.Now().UTC().Format(time.RFC1123)
	headers := map[string]string{
		"x-ms-date":    now,
		"x-ms-version": "2020-10-02",
	}

	// Sign the request
	authHeader, err := a.signRequest("HEAD", blobPath, headers, 0)
	if err != nil {
		return nil, err
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, fmt.Errorf("azure: create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", authHeader)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-date", now)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-version", "2020-10-02")

	// Execute request
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure: head request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil //nolint:nilnil // standard Go not-found pattern: nil pointer with nil error
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("azure: head failed with status %d", resp.StatusCode) //nolint:err113 // dynamic error with runtime context
	}

	// Extract metadata from x-ms-meta-* headers
	metadata := make(map[string]string)

	for key, values := range resp.Header {
		if after, ok := strings.CutPrefix(key, "X-Ms-Meta-"); ok {
			metaKey := after
			if len(values) > 0 {
				metadata[metaKey] = values[0]
			}
		}
	}

	return metadata, nil
}

// List returns a list of blob identifiers with optional filtering.
// Listing is not supported for Azure Blob Storage; this always returns an empty list.
func (a *AzureBlobStore) List(ctx context.Context, prefix string, limit int, offset int) ([]string, error) {
	// Azure Blob Storage requires XML parsing for list operations.
	// Listing is not supported for Azure; return an empty list.
	return []string{}, nil
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

// PresignURL returns a presigned URL for direct blob download.
// Azure Blob Storage doesn't support presigned URLs in the same way as S3,
// so this returns an error.
func (a *AzureBlobStore) PresignURL(ctx context.Context, sha256 string, ttl time.Duration) (string, error) {
	if err := ValidateBlobID(sha256); err != nil {
		return "", err
	}

	return "", ErrPresignNotSupported
}

// HealthCheck verifies that the Azure container is accessible.
func (a *AzureBlobStore) HealthCheck(ctx context.Context) error {
	url := a.baseURL + "?restype=container"

	// Prepare headers
	now := time.Now().UTC().Format(time.RFC1123)
	headers := map[string]string{
		"x-ms-date":    now,
		"x-ms-version": "2020-10-02",
	}

	// Sign the request (special case for container check)
	authHeader, err := a.signRequest("GET", "?restype=container", headers, 0)
	if err != nil {
		return err
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("azure: create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", authHeader)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-date", now)
	//nolint:canonicalheader // Azure REST API requires lowercase headers
	req.Header.Set("x-ms-version", "2020-10-02")

	// Execute request
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("azure: health check request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("azure: health check failed with status %d", resp.StatusCode) //nolint:err113 // dynamic error with runtime context
	}

	return nil
}
