// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// azureUploadBlockSize is the block size of AppendUpload: the most it holds
	// in memory per request. Azure has no minimum block size, so a short tail
	// is a block like any other.
	azureUploadBlockSize = 8 << 20

	// azureMaxBlocks is the most blocks a blob can have.
	azureMaxBlocks = 50000
)

var _ UploadStore = (*AzureBlobStore)(nil)

// azureUploadState is the opaque State of an upload: how many blocks (ids 1..N,
// see azureBlockID) hold its bytes.
type azureUploadState struct {
	Blocks int `json:"n"`
}

// UploadsSupported reports that uploads are the blobs uploads/<id> of the
// container.
func (a *AzureBlobStore) UploadsSupported() bool { return true }

// parseAzureUploadState decodes a State; "" is a new upload.
func parseAzureUploadState(state string) (azureUploadState, error) {
	var st azureUploadState

	if state == "" {
		return st, nil
	}

	if err := json.Unmarshal([]byte(state), &st); err != nil || st.Blocks < 0 || st.Blocks > azureMaxBlocks {
		return st, fmt.Errorf("%w: invalid upload state %q", ErrUploadDataLost, state)
	}

	return st, nil
}

// AppendUpload stages the bytes of r as the blocks n+1, n+2, ... of the blob
// uploads/<id>, where n is the block count of at. Staged blocks are invisible
// until CompleteUpload commits them, and the position only references blocks
// 1..n, so a failed append (and the blocks it staged) is harmless: the retry
// stages the same ids again, which overwrites them, and blocks past the new end
// are ignored at completion. Nothing is dropped after commit, there is
// nothing superseded.
//
// The block count is not checked against the service here (it would cost a
// request per call); CompleteUpload does.
func (a *AzureBlobStore) AppendUpload(
	ctx context.Context,
	id string,
	at UploadPosition,
	r io.Reader,
	commit func(ctx context.Context, state string, n int64) error,
) error {
	key, err := uploadKey(id)
	if err != nil {
		return err
	}

	st, err := parseAzureUploadState(at.State)
	if err != nil {
		return err
	}

	bufp := a.getUploadBuffer()
	defer a.putUploadBuffer(bufp)

	buf := *bufp

	var total int64

	blocks := st.Blocks

	for {
		n, err := io.ReadFull(r, buf)
		last := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)

		if err != nil && !last {
			return fmt.Errorf("azure: read upload body: %w", err)
		}

		if n > 0 {
			if blocks == azureMaxBlocks {
				return ErrUploadTooLarge
			}

			blocks++

			if err := a.putBlock(ctx, key, azureBlockID(blocks), buf[:n]); err != nil {
				return err
			}

			total += int64(n)
		}

		if last {
			break
		}
	}

	if total == 0 {
		return nil
	}

	state, err := json.Marshal(azureUploadState{Blocks: blocks})
	if err != nil {
		return fmt.Errorf("azure: encode upload state: %w", err)
	}

	return commit(ctx, string(state), total)
}

func (a *AzureBlobStore) getUploadBuffer() *[]byte {
	if a.uploadBuffers != nil {
		if bufp, _ := a.uploadBuffers.Get().(*[]byte); bufp != nil {
			return bufp
		}
	}

	b := make([]byte, azureUploadBlockSize)

	return &b
}

func (a *AzureBlobStore) putUploadBuffer(bufp *[]byte) {
	if a.uploadBuffers != nil {
		a.uploadBuffers.Put(bufp)
	}
}

// putBlock stages data as the uncommitted block blockID of the blob key (Put
// Block). Staging an id again replaces the block.
func (a *AzureBlobStore) putBlock(ctx context.Context, key, blockID string, data []byte) error {
	resp, err := a.do(ctx, azureCall{
		method: http.MethodPut,
		blob:   key,
		query:  url.Values{"comp": {"block"}, "blockid": {blockID}},
		body:   bytes.NewReader(data),
		length: int64(len(data)),
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if !azureOK(resp) {
		return azureStatusError("put block", resp)
	}

	return nil
}

// CompleteUpload commits blocks 1..n as the blob uploads/<id> (Put Block List)
// after checking that each is staged and that together they hold exactly
// at.Offset bytes; blocks staged beyond n, left by failed appends, are dropped
// by the commit. The version is the ETag of the committed blob.
//
// It is idempotent: once committed there are no uncommitted blocks left, and a
// blob of at.Offset bytes is the result of an earlier call.
func (a *AzureBlobStore) CompleteUpload(ctx context.Context, id string, at UploadPosition) (StagedObject, error) {
	key, err := uploadKey(id)
	if err != nil {
		return StagedObject{}, err
	}

	st, err := parseAzureUploadState(at.State)
	if err != nil {
		return StagedObject{}, err
	}

	staged, err := a.uncommittedBlocks(ctx, key)
	if err != nil {
		return StagedObject{}, err
	}

	if len(staged) == 0 {
		return a.completedUpload(ctx, key, st, at)
	}

	blockIDs := make([]string, 0, st.Blocks)

	var sum int64

	for i := 1; i <= st.Blocks; i++ {
		blockID := azureBlockID(i)

		size, ok := staged[blockID]
		if !ok {
			return StagedObject{}, fmt.Errorf("%w: block %d of %s is not staged", ErrUploadDataLost, i, id)
		}

		blockIDs = append(blockIDs, blockID)
		sum += size
	}

	if sum != at.Offset {
		return StagedObject{}, fmt.Errorf("%w: %s holds %d bytes, want %d", ErrUploadDataLost, id, sum, at.Offset)
	}

	return a.commitUpload(ctx, key, blockIDs, at.Offset)
}

// completedUpload answers CompleteUpload when no block is staged: the blob is
// there from an earlier call, or the upload is empty and still to be created.
func (a *AzureBlobStore) completedUpload(ctx context.Context, key string, st azureUploadState, at UploadPosition) (StagedObject, error) {
	obj, err := a.statBlob(ctx, key)

	switch {
	case err == nil:
		if obj.Size != at.Offset {
			return StagedObject{}, fmt.Errorf("%w: %s holds %d bytes, want %d", ErrUploadDataLost, key, obj.Size, at.Offset)
		}

		return obj, nil
	case !errors.Is(err, ErrBlobNotFound):
		return StagedObject{}, err
	case st.Blocks == 0 && at.Offset == 0:
		return a.commitUpload(ctx, key, nil, 0)
	}

	return StagedObject{}, fmt.Errorf("%w: %s has no data", ErrUploadDataLost, key)
}

// commitUpload commits blockIDs as the blob key and describes it.
func (a *AzureBlobStore) commitUpload(ctx context.Context, key string, blockIDs []string, size int64) (StagedObject, error) {
	etag, err := a.putBlockList(ctx, key, blockIDs)
	if err != nil {
		return StagedObject{}, err
	}

	if etag == "" {
		return StagedObject{}, fmt.Errorf("%w: put block list %s returned no ETag", ErrAzureUnexpectedStatus, key)
	}

	return StagedObject{Size: size, Version: etag}, nil
}

// PromoteUpload copies the completed upload to the content-addressed blob with
// a server-side Copy Blob, exactly as PromoteStaging does for a staging
// object.
func (a *AzureBlobStore) PromoteUpload(ctx context.Context, id, sha256, version string, metadata map[string]string) error {
	src, err := uploadKey(id)
	if err != nil {
		return err
	}

	return a.promoteBlob(ctx, src, sha256, version, metadata)
}

// OpenUpload reads the completed upload, conditional on its ETag.
func (a *AzureBlobStore) OpenUpload(ctx context.Context, id, version string) (io.ReadCloser, error) {
	key, err := uploadKey(id)
	if err != nil {
		return nil, err
	}

	return a.openBlobIfMatch(ctx, key, version)
}

// DeleteUpload removes the completed upload; a missing one is not an error.
// Blocks staged for an upload that was never completed do not exist for Delete
// Blob: Azure discards them by itself after a week.
func (a *AzureBlobStore) DeleteUpload(ctx context.Context, id string) error {
	key, err := uploadKey(id)
	if err != nil {
		return err
	}

	return a.deleteBlob(ctx, key)
}

// WalkUploads lists the completed uploads last modified before olderThan. An
// upload that was never completed has no blob to list. Blobs without a
// readable modification time or an upload id as name are skipped.
func (a *AzureBlobStore) WalkUploads(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	marker := ""

	for {
		page, err := a.listBlobs(ctx, uploadPrefix, marker, azureListPageSize)
		if err != nil {
			return err
		}

		var ids []string

		for _, b := range page.Blobs {
			modified, err := http.ParseTime(b.Properties.LastModified)
			if err != nil || !modified.Before(olderThan) {
				continue
			}

			id := strings.TrimPrefix(b.Name, uploadPrefix)
			if validUploadID(id) != nil {
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
