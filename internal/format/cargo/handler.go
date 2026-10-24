// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/openartifact/openartifact/internal/format"
)

type CargoHandler struct{}

// errMetadataTooLarge reports a publish frame declaring more metadata than
// maxPublishMetadataBytes.
var errMetadataTooLarge = errors.New("metadata is over the size limit")

// readMetadata reads the length-prefixed metadata JSON that opens a publish
// frame (4-byte LE uint32, then that many bytes). The length is the client's
// claim: it is capped, and the buffer grows as the bytes arrive, so four bytes
// cannot make the caller allocate gigabytes.
func readMetadata(r io.Reader) ([]byte, error) {
	var metadataLen uint32
	if err := binary.Read(r, binary.LittleEndian, &metadataLen); err != nil {
		return nil, fmt.Errorf("failed to read metadata length: %w", err)
	}

	if metadataLen > maxPublishMetadataBytes {
		return nil, fmt.Errorf("%w: %d bytes declared, %d allowed", errMetadataTooLarge, metadataLen, maxPublishMetadataBytes)
	}

	metadata, err := readDeclared(r, metadataLen)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	return metadata, nil
}

func NewCargoHandler() *CargoHandler {
	return &CargoHandler{}
}

func (h *CargoHandler) Name() string {
	return "cargo"
}

func (h *CargoHandler) GetContentType() string {
	return "application/octet-stream"
}

func (h *CargoHandler) GetFileExtension() string {
	return ".crate"
}

func (h *CargoHandler) Validate(ctx context.Context, r io.Reader) error {
	metadataBytes, err := readMetadata(r)
	if err != nil {
		return fmt.Errorf("invalid cargo crate: %w", err)
	}

	// Validate metadata JSON
	var metadata map[string]any
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return fmt.Errorf("invalid cargo crate: metadata is not valid JSON: %w", err)
	}

	// Check required fields
	if _, ok := metadata["name"]; !ok {
		return errors.New("invalid cargo crate: missing 'name' field") //nolint:err113 // static error
	}

	if _, ok := metadata["vers"]; !ok {
		return errors.New("invalid cargo crate: missing 'vers' field") //nolint:err113 // static error
	}

	return nil
}

func (h *CargoHandler) Parse(ctx context.Context, r io.Reader) (map[string]any, error) {
	metadataBytes, err := readMetadata(r)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cargo crate: %w", err)
	}

	// Parse metadata JSON
	var metadata map[string]any
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse cargo crate: metadata is not valid JSON: %w", err)
	}

	return metadata, nil
}

var _ format.FormatHandler = (*CargoHandler)(nil)
