// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// chunkRows is the in-memory docker_upload_chunks table of a fake database:
// the chunk rows of each session in the order they were appended, removed with
// the session like the real foreign key cascade.
type chunkRows struct {
	mu   sync.Mutex
	rows map[string][]models.DockerUploadChunk
}

func (c *chunkRows) append(repositoryID, uploadUUID, chunkID string, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.rows == nil {
		c.rows = map[string][]models.DockerUploadChunk{}
	}

	key := repositoryID + "|" + uploadUUID
	c.rows[key] = append(c.rows[key], models.DockerUploadChunk{ChunkID: chunkID, Size: size})
}

func (c *chunkRows) list(repositoryID, uploadUUID string) []models.DockerUploadChunk {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]models.DockerUploadChunk(nil), c.rows[repositoryID+"|"+uploadUUID]...)
}

func (c *chunkRows) drop(repositoryID, uploadUUID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.rows, repositoryID+"|"+uploadUUID)
}

// chunkObjects lists the ids of every chunk object the handler's chunk store
// holds.
func chunkObjects(t *testing.T, h *HTTPHandler) []string {
	t.Helper()

	var ids []string

	err := h.chunks.WalkChunks(context.Background(), time.Now().Add(time.Hour), func(batch []string) error {
		ids = append(ids, batch...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk chunks: %v", err)
	}

	return ids
}
