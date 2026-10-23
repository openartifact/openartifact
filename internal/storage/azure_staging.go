// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// azureMaxPutBlob is the largest body of a single Put Blob (x-ms-version
	// 2019-12-12 and later).
	azureMaxPutBlob int64 = 5000 << 20

	// azureListPageSize is the page size of the staging walk.
	azureListPageSize = 5000

	// azureCopyPollInterval is the first wait between polls of a pending copy;
	// it doubles up to azureCopyPollMax.
	azureCopyPollInterval = 250 * time.Millisecond
	azureCopyPollMax      = 5 * time.Second

	// azureCopyMaxWait bounds how long a promotion waits for a copy to finish.
	azureCopyMaxWait = time.Hour

	// azureCleanupTimeout bounds the best-effort removal of a partial copy.
	azureCleanupTimeout = 30 * time.Second

	// azurePartListCap is how many part numbers an incomplete-upload error lists.
	azurePartListCap = 10

	azureCopySuccess = "success"
	azureCopyPending = "pending"
)

// ErrAzureCopyFailed is returned when Azure reports a server-side copy as
// failed or aborted.
var ErrAzureCopyFailed = errors.New("azure: server-side copy failed")

var (
	_ StagingStore          = (*AzureBlobStore)(nil)
	_ MultipartStagingStore = (*AzureBlobStore)(nil)
)

// StagingSupported reports that the Azure backend has a staging area: the
// blobs under staging/ in the same container.
func (a *AzureBlobStore) StagingSupported() bool { return true }

// PresignStagingPut returns a SAS URL (permissions create + write) for a single
// Put Blob of the staging object id. A SAS cannot sign the body length, so the
// size is checked against the staging object by the caller (StatStaging).
// Objects above the Put Blob limit need a multipart upload.
func (a *AzureBlobStore) PresignStagingPut(_ context.Context, id string, size int64, ttl time.Duration) (PresignedPut, error) {
	key, err := stagingKey(id)
	if err != nil {
		return PresignedPut{}, err
	}

	if size > azureMaxPutBlob {
		return PresignedPut{}, fmt.Errorf("%w: %d bytes exceed the Azure Put Blob limit of %d", ErrSingleUploadTooLarge, size, azureMaxPutBlob)
	}

	expires := a.now().Add(ClampStagingTTL(ttl)).UTC().Truncate(time.Second)

	return PresignedPut{
		URL:     a.sasURL(azureSAS{blob: key, permissions: "cw", expiry: expires}, nil),
		Headers: map[string]string{"x-ms-blob-type": "BlockBlob"},
		Expires: expires,
	}, nil
}

// StatStaging describes a staging object; its ETag is the version.
func (a *AzureBlobStore) StatStaging(ctx context.Context, id string) (StagedObject, error) {
	key, err := stagingKey(id)
	if err != nil {
		return StagedObject{}, err
	}

	h, err := a.headBlob(ctx, key)
	if err != nil {
		if errors.Is(err, ErrBlobNotFound) {
			return StagedObject{}, ErrStagingNotFound
		}

		return StagedObject{}, err
	}

	size, err := azureContentLength(h)
	if err != nil {
		return StagedObject{}, fmt.Errorf("azure: head %s: %w", key, err)
	}

	etag := h.Get("ETag")
	if etag == "" {
		return StagedObject{}, fmt.Errorf("%w: head %s returned no ETag", ErrAzureUnexpectedStatus, key)
	}

	return StagedObject{Size: size, Version: etag}, nil
}

// OpenStaging reads a staging object, conditional on its ETag.
func (a *AzureBlobStore) OpenStaging(ctx context.Context, id, version string) (io.ReadCloser, error) {
	key, err := stagingKey(id)
	if err != nil {
		return nil, err
	}

	if version == "" {
		return nil, fmt.Errorf("%w: no version to open", ErrStagingChanged)
	}

	resp, err := a.do(ctx, azureCall{method: http.MethodGet, blob: key, header: map[string]string{"If-Match": version}})
	if err != nil {
		return nil, err
	}

	switch {
	case azureOK(resp):
		return resp.Body, nil
	case resp.StatusCode == http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, ErrStagingNotFound
	case resp.StatusCode == http.StatusPreconditionFailed:
		_ = resp.Body.Close()
		return nil, ErrStagingChanged
	}

	defer func() { _ = resp.Body.Close() }()

	return nil, azureStatusError("get", resp)
}

// PromoteStaging copies a staging object to the content-addressed blob with a
// server-side Copy Blob (the bytes never pass through OpenArtifact). The copy is
// conditional on the staging object still having the ETag that was verified
// (x-ms-source-if-match), so an overwrite through the still-valid SAS after
// verification cannot put unverified bytes under a content-addressed name.
//
// An existing blob is left alone, unless it is the leftover of a failed copy
// (removed and copied again) or of a copy still pending (awaited). The copy
// inherits the staging blob's properties and, when no metadata is given, its
// metadata, both chosen by the uploader, so they are reset to what Put writes.
func (a *AzureBlobStore) PromoteStaging(ctx context.Context, id, sha256, version string, metadata map[string]string) error {
	src, err := stagingKey(id)
	if err != nil {
		return err
	}

	if err := ValidateBlobID(sha256); err != nil {
		return err
	}

	if version == "" {
		return fmt.Errorf("%w: no version to promote", ErrStagingChanged)
	}

	dst := azureBlobKey(sha256)

	h, err := a.headBlob(ctx, dst)

	switch {
	case err == nil:
		switch h.Get("X-Ms-Copy-Status") {
		case "", azureCopySuccess:
			return nil
		case azureCopyPending:
			return a.awaitCopy(ctx, dst)
		}

		// A failed or aborted copy left a partial blob behind.
		if err := a.deleteBlob(ctx, dst); err != nil {
			return err
		}
	case !errors.Is(err, ErrBlobNotFound):
		return err
	}

	headers := azureMetaHeaders(metadata)
	noMetadata := len(headers) == 0
	headers["x-ms-copy-source"] = a.blobURL(src)
	headers["x-ms-source-if-match"] = version

	status, err := a.startCopy(ctx, dst, headers)
	if err != nil {
		return err
	}

	if status != azureCopySuccess {
		if err := a.awaitCopy(ctx, dst); err != nil {
			return err
		}
	}

	if err := a.resetCopiedBlob(ctx, dst, noMetadata); err != nil {
		a.discardBlob(ctx, dst)
		return err
	}

	return nil
}

// startCopy sends Copy Blob and returns the copy status of the response.
func (a *AzureBlobStore) startCopy(ctx context.Context, dst string, headers map[string]string) (string, error) {
	resp, err := a.do(ctx, azureCall{method: http.MethodPut, blob: dst, header: headers})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", ErrStagingNotFound
	case resp.StatusCode == http.StatusPreconditionFailed:
		return "", ErrStagingChanged
	case resp.StatusCode == http.StatusConflict:
		// Another promotion has a copy to dst pending.
		return azureCopyPending, nil
	case !azureOK(resp):
		return "", azureStatusError("copy", resp)
	}

	return resp.Header.Get("X-Ms-Copy-Status"), nil
}

// awaitCopy waits for the copy to blob to finish. A copy that failed, was
// aborted or could not be awaited (ctx done, timeout) leaves no partial blob.
func (a *AzureBlobStore) awaitCopy(ctx context.Context, blob string) error {
	waitCtx, cancel := context.WithTimeout(ctx, azureCopyMaxWait)
	defer cancel()

	if err := a.pollCopy(waitCtx, blob); err != nil {
		a.discardBlob(ctx, blob)
		return err
	}

	return nil
}

// pollCopy polls the copy status of blob until it is no longer pending.
func (a *AzureBlobStore) pollCopy(ctx context.Context, blob string) error {
	interval := a.copyPollInterval

	for {
		h, err := a.headBlob(ctx, blob)
		if err != nil {
			return err
		}

		switch status := h.Get("X-Ms-Copy-Status"); status {
		case "", azureCopySuccess:
			return nil
		case azureCopyPending:
		default:
			return fmt.Errorf("%w: %s to %s: %s", ErrAzureCopyFailed, status, blob, h.Get("X-Ms-Copy-Status-Description"))
		}

		timer := time.NewTimer(interval)

		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("azure: waiting for copy to %s: %w", blob, ctx.Err())
		case <-timer.C:
		}

		interval = min(interval*2, azureCopyPollMax)
	}
}

// discardBlob removes a partial or unwanted blob, best effort and even when ctx
// is already done: whatever stays behind is dealt with by the next promotion
// (HEAD shows the copy status) or the cleanup worker.
func (a *AzureBlobStore) discardBlob(ctx context.Context, blob string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), azureCleanupTimeout)
	defer cancel()

	_ = a.deleteBlob(ctx, blob)
}

// resetCopiedBlob gives a copied blob the properties Put writes (content type
// application/octet-stream, nothing else) and clears the metadata it inherited
// when the promotion supplied none.
func (a *AzureBlobStore) resetCopiedBlob(ctx context.Context, blob string, clearMetadata bool) error {
	if err := a.putBlobComp(ctx, blob, "properties", map[string]string{"x-ms-blob-content-type": "application/octet-stream"}); err != nil {
		return err
	}

	if clearMetadata {
		return a.putBlobComp(ctx, blob, "metadata", nil)
	}

	return nil
}

// putBlobComp sends a body-less PUT <blob>?comp=<comp> (Set Blob Properties,
// Set Blob Metadata).
func (a *AzureBlobStore) putBlobComp(ctx context.Context, blob, comp string, headers map[string]string) error {
	resp, err := a.do(ctx, azureCall{method: http.MethodPut, blob: blob, query: url.Values{"comp": {comp}}, header: headers})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if !azureOK(resp) {
		return azureStatusError("set blob "+comp, resp)
	}

	return nil
}

// DeleteStaging removes a staging object; a missing object is not an error. A
// blob that only has uncommitted blocks (an unfinished multipart upload) does
// not exist for Delete Blob: Azure discards such blocks by itself after a week.
func (a *AzureBlobStore) DeleteStaging(ctx context.Context, id string) error {
	key, err := stagingKey(id)
	if err != nil {
		return err
	}

	return a.deleteBlob(ctx, key)
}

// WalkStaging lists staging objects last modified before olderThan. Blobs
// without a readable modification time are skipped.
func (a *AzureBlobStore) WalkStaging(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	marker := ""

	for {
		page, err := a.listBlobs(ctx, stagingPrefix, marker, azureListPageSize)
		if err != nil {
			return err
		}

		var ids []string

		for _, b := range page.Blobs {
			modified, err := http.ParseTime(b.Properties.LastModified)
			if err != nil || !modified.Before(olderThan) {
				continue
			}

			id := strings.TrimPrefix(b.Name, stagingPrefix)
			if _, err := stagingKey(id); err != nil {
				continue
			}

			ids = append(ids, id)
		}

		if len(ids) > 0 {
			if err := fn(ids); err != nil {
				return err
			}
		}

		if page.NextMarker == "" {
			return nil
		}

		marker = page.NextMarker
	}
}

// MultipartSupported reports that the Azure backend stages multipart uploads as
// block blobs.
func (a *AzureBlobStore) MultipartSupported() bool { return true }

// InitiateMultipart opens a multipart upload. Azure needs no server-side
// upload object: blocks are staged on the blob itself, so the handle is empty.
func (a *AzureBlobStore) InitiateMultipart(_ context.Context, id string, _, _ int64) (string, error) {
	if _, err := stagingKey(id); err != nil {
		return "", err
	}

	return "", nil
}

// azureBlockID is the block id of part number: the base64 of a fixed-width
// decimal (all block ids of a blob must have the same length).
func azureBlockID(number int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%08d", number)))
}

// PresignStagingPart returns a SAS URL (permission write) for Put Block of part
// number of the staging object id. A SAS cannot sign the block length, so
// CompleteMultipart checks the sizes.
func (a *AzureBlobStore) PresignStagingPart(_ context.Context, id, _ string, number int, _ int64, ttl time.Duration) (PresignedPut, error) {
	key, err := stagingKey(id)
	if err != nil {
		return PresignedPut{}, err
	}

	if number < 1 || number > MaxParts {
		return PresignedPut{}, fmt.Errorf("%w: part number %d is outside 1..%d", ErrInvalidPartPlan, number, MaxParts)
	}

	expires := a.now().Add(ClampStagingTTL(ttl)).UTC().Truncate(time.Second)
	block := url.Values{"comp": {"block"}, "blockid": {azureBlockID(number)}}

	return PresignedPut{
		URL:     a.sasURL(azureSAS{blob: key, permissions: "w", expiry: expires}, block),
		Headers: map[string]string{},
		Expires: expires,
	}, nil
}

// azureBlockList is the subset of the Get Block List response the store needs.
type azureBlockList struct {
	Uncommitted []struct {
		Name string `xml:"Name"`
		Size int64  `xml:"Size"`
	} `xml:"UncommittedBlocks>Block"`
}

// CompleteMultipart checks that every planned block was staged with its planned
// size and commits them as the staging blob (Put Block List). It is idempotent:
// a committed staging blob of the right size means a previous call did it.
func (a *AzureBlobStore) CompleteMultipart(ctx context.Context, id, _ string, size, partSize int64) error {
	key, err := stagingKey(id)
	if err != nil {
		return err
	}

	h, err := a.headBlob(ctx, key)

	switch {
	case err == nil:
		if got, lenErr := azureContentLength(h); lenErr == nil && got == size {
			return nil
		}
	case !errors.Is(err, ErrBlobNotFound):
		return err
	}

	staged, err := a.uncommittedBlocks(ctx, key)
	if err != nil {
		return err
	}

	blockIDs, err := plannedBlockIDs(staged, size, partSize)
	if err != nil {
		return err
	}

	if err := a.putBlockList(ctx, key, blockIDs); err != nil {
		return err
	}

	h, err = a.headBlob(ctx, key)
	if err != nil {
		return fmt.Errorf("azure: verify assembled %s: %w", key, err)
	}

	got, err := azureContentLength(h)
	if err != nil {
		return fmt.Errorf("azure: verify assembled %s: %w", key, err)
	}

	if got != size {
		return fmt.Errorf("%w: the assembled object has %d bytes, want %d", ErrMultipartIncomplete, got, size)
	}

	return nil
}

// uncommittedBlocks returns the staged, uncommitted blocks of blob by block id
// with their sizes. A blob that does not exist has none.
func (a *AzureBlobStore) uncommittedBlocks(ctx context.Context, blob string) (map[string]int64, error) {
	resp, err := a.do(ctx, azureCall{
		method: http.MethodGet,
		blob:   blob,
		query:  url.Values{"comp": {"blocklist"}, "blocklisttype": {"uncommitted"}},
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return map[string]int64{}, nil
	}

	if !azureOK(resp) {
		return nil, azureStatusError("get block list", resp)
	}

	var list azureBlockList
	if err := xml.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("azure: decode block list: %w", err)
	}

	staged := make(map[string]int64, len(list.Uncommitted))
	for _, b := range list.Uncommitted {
		staged[b.Name] = b.Size
	}

	return staged, nil
}

// plannedBlockIDs returns the block ids of parts 1..PartCount in order, or
// ErrMultipartIncomplete naming the parts that are missing or have the wrong
// size. The one empty part of an empty object has no block.
func plannedBlockIDs(staged map[string]int64, size, partSize int64) ([]string, error) {
	count := PartCount(size, partSize)
	blockIDs := make([]string, 0, count)

	var missing, wrong []int

	for n := 1; n <= count; n++ {
		want := PartLength(size, partSize, n)
		if want == 0 {
			continue
		}

		blockID := azureBlockID(n)

		switch got, ok := staged[blockID]; {
		case !ok:
			missing = append(missing, n)
		case got != want:
			wrong = append(wrong, n)
		}

		blockIDs = append(blockIDs, blockID)
	}

	if len(missing) == 0 && len(wrong) == 0 {
		return blockIDs, nil
	}

	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "missing parts "+partList(missing))
	}

	if len(wrong) > 0 {
		problems = append(problems, "parts with the wrong size "+partList(wrong))
	}

	return nil, fmt.Errorf("%w: %s", ErrMultipartIncomplete, strings.Join(problems, "; "))
}

// partList renders part numbers, the first azurePartListCap of them.
func partList(numbers []int) string {
	shown := numbers[:min(len(numbers), azurePartListCap)]
	parts := make([]string, len(shown))

	for i, n := range shown {
		parts[i] = strconv.Itoa(n)
	}

	list := strings.Join(parts, ", ")
	if more := len(numbers) - len(shown); more > 0 {
		list += " and " + strconv.Itoa(more) + " more"
	}

	return list
}

// putBlockList commits the blocks, in order, as the content of blob (Put Block
// List). Latest picks the uncommitted block of an id over a committed one.
func (a *AzureBlobStore) putBlockList(ctx context.Context, blob string, blockIDs []string) error {
	var sb strings.Builder

	sb.WriteString(`<?xml version="1.0" encoding="utf-8"?><BlockList>`)

	for _, id := range blockIDs {
		sb.WriteString("<Latest>" + id + "</Latest>")
	}

	sb.WriteString("</BlockList>")

	body := sb.String()

	resp, err := a.do(ctx, azureCall{
		method: http.MethodPut,
		blob:   blob,
		query:  url.Values{"comp": {"blocklist"}},
		header: map[string]string{"Content-Type": "application/xml", "x-ms-blob-content-type": "application/octet-stream"},
		body:   strings.NewReader(body),
		length: int64(len(body)),
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if !azureOK(resp) {
		return azureStatusError("put block list", resp)
	}

	return nil
}
