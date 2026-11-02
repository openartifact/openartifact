// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// azureChunkBlockSize is the block size of a streamed chunk: the most a
	// PutChunk holds in memory, and the size at which a body switches from one
	// Put Blob to staged blocks.
	azureChunkBlockSize = 8 << 20

	// azureMaxBlocks is the most blocks a blob can have.
	azureMaxBlocks = 50000
)

// ErrAzureChunkTooLarge is returned by PutChunk for a body that needs more
// than azureMaxBlocks blocks.
var ErrAzureChunkTooLarge = errors.New("azure: upload chunk exceeds the block limit of a blob")

var _ ChunkStore = (*AzureBlobStore)(nil)

// ChunksSupported implements ChunkStore: chunks are the blobs uploads/<id> of
// the container.
func (a *AzureBlobStore) ChunksSupported() bool { return true }

// PutChunk implements ChunkStore. A body that fits one block is stored with a
// single Put Blob; a longer one is cut into azureChunkBlockSize blocks that are
// staged one after the other from a single reused buffer and committed with
// one Put Block List, so memory use is bounded and nothing is spooled to disk.
// Uncommitted blocks are invisible and Azure discards them by itself, so a
// failed write leaves no chunk behind.
func (a *AzureBlobStore) PutChunk(ctx context.Context, id string, r io.Reader) (int64, error) {
	key, err := chunkKey(id)
	if err != nil {
		return 0, err
	}

	var bufp *[]byte
	if a.chunkBuffers != nil {
		bufp, _ = a.chunkBuffers.Get().(*[]byte)
	}

	if bufp == nil {
		b := make([]byte, azureChunkBlockSize)
		bufp = &b
	}

	if a.chunkBuffers != nil {
		defer a.chunkBuffers.Put(bufp)
	}

	n, err := a.putChunkBlocks(ctx, key, r, *bufp)
	if err != nil {
		a.discardBlob(ctx, key)

		return 0, err
	}

	return n, nil
}

// putChunkBlocks stores r as the blob key, reading it through buf.
func (a *AzureBlobStore) putChunkBlocks(ctx context.Context, key string, r io.Reader, buf []byte) (int64, error) {
	var (
		total    int64
		blockIDs []string
	)

	for {
		n, err := io.ReadFull(r, buf)
		last := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)

		if err != nil && !last {
			return 0, fmt.Errorf("azure: read chunk body: %w", err)
		}

		if last && len(blockIDs) == 0 {
			return int64(n), a.putChunkBlob(ctx, key, buf[:n])
		}

		if n > 0 {
			if len(blockIDs) == azureMaxBlocks {
				return 0, ErrAzureChunkTooLarge
			}

			blockID := azureBlockID(len(blockIDs) + 1)
			if err := a.putChunkBlock(ctx, key, blockID, buf[:n]); err != nil {
				return 0, err
			}

			blockIDs = append(blockIDs, blockID)
			total += int64(n)
		}

		if last {
			return total, a.putBlockList(ctx, key, blockIDs)
		}
	}
}

// putChunkBlob stores data as the whole blob (Put Blob).
func (a *AzureBlobStore) putChunkBlob(ctx context.Context, key string, data []byte) error {
	resp, err := a.do(ctx, azureCall{
		method: http.MethodPut,
		blob:   key,
		header: map[string]string{"x-ms-blob-type": "BlockBlob", "Content-Type": "application/octet-stream"},
		body:   bytes.NewReader(data),
		length: int64(len(data)),
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if !azureOK(resp) {
		return azureStatusError("put chunk", resp)
	}

	return nil
}

// putChunkBlock stages data as the uncommitted block blockID of the blob key
// (Put Block).
func (a *AzureBlobStore) putChunkBlock(ctx context.Context, key, blockID string, data []byte) error {
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
		return azureStatusError("put chunk block", resp)
	}

	return nil
}

// OpenChunk implements ChunkStore.
func (a *AzureBlobStore) OpenChunk(ctx context.Context, id string) (io.ReadCloser, error) {
	key, err := chunkKey(id)
	if err != nil {
		return nil, err
	}

	resp, err := a.do(ctx, azureCall{method: http.MethodGet, blob: key})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()

		return nil, fmt.Errorf("%w: %s", ErrChunkNotFound, id)
	}

	if !azureOK(resp) {
		defer func() { _ = resp.Body.Close() }()

		return nil, azureStatusError("get chunk", resp)
	}

	return resp.Body, nil
}

// DeleteChunk implements ChunkStore.
func (a *AzureBlobStore) DeleteChunk(ctx context.Context, id string) error {
	key, err := chunkKey(id)
	if err != nil {
		return err
	}

	return a.deleteBlob(ctx, key)
}

// WalkChunks implements ChunkStore. Blobs without a readable modification time
// are skipped.
func (a *AzureBlobStore) WalkChunks(ctx context.Context, olderThan time.Time, fn func(ids []string) error) error {
	marker := ""

	for {
		page, err := a.listBlobs(ctx, chunkPrefix, marker, azureListPageSize)
		if err != nil {
			return err
		}

		var ids []string

		for _, b := range page.Blobs {
			modified, err := http.ParseTime(b.Properties.LastModified)
			if err != nil || !modified.Before(olderThan) {
				continue
			}

			id := strings.TrimPrefix(b.Name, chunkPrefix)
			if validChunkID(id) != nil {
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
