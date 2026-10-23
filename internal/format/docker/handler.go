// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/openartifact/openartifact/internal/format"
	"github.com/openartifact/openartifact/internal/format/base"
)

type DockerHandler struct{}

func NewDockerHandler() *DockerHandler {
	return &DockerHandler{}
}

func (h *DockerHandler) Name() string {
	return "docker"
}

func (h *DockerHandler) GetContentType() string {
	return "application/vnd.docker.distribution.manifest.v2+json"
}

func (h *DockerHandler) GetFileExtension() string {
	return ".json"
}

func (h *DockerHandler) Validate(ctx context.Context, r io.Reader) error {
	data, err := base.ReadAllLimited(r, maxManifestBytes)
	if err != nil {
		return fmt.Errorf("failed to read manifest: %w", err)
	}

	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("invalid JSON manifest: %w", err)
	}

	// Check for required fields: mediaType or schemaVersion
	if _, hasMediaType := manifest["mediaType"]; !hasMediaType {
		if _, hasSchemaVersion := manifest["schemaVersion"]; !hasSchemaVersion {
			return errors.New("manifest must contain mediaType or schemaVersion field") //nolint:err113 // static error
		}
	}

	return nil
}

func (h *DockerHandler) Parse(ctx context.Context, r io.Reader) (map[string]any, error) {
	data, err := base.ReadAllLimited(r, maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	return manifest, nil
}

var _ format.FormatHandler = (*DockerHandler)(nil)
