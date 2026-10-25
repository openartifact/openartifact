// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/openartifact/openartifact/internal/pathseg"
)

const maxChartYAMLSize = 1 << 20 // 1 MiB

// Sentinel errors for Helm chart parsing.
var (
	ErrChartYAMLNotFound = errors.New("chart.yaml not found in archive")
	ErrChartYAMLTooLarge = errors.New("chart.yaml exceeds max size")
)

// readAllLimited reads at most max+1 bytes from r and errors if the
// result exceeds max, without ever allocating more than max+1 bytes.
func readAllLimited(r io.Reader, limit int64, tooLarge error) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: max %d bytes", tooLarge, limit)
	}

	return data, nil
}

// maxUnpackedChartSize bounds the decompressed bytes scanned while looking for
// Chart.yaml, so a gzip bomb cannot burn CPU before the entry is found.
const maxUnpackedChartSize = 1 << 30 // 1 GiB

// ErrChartTooLarge reports a chart whose unpacked size exceeds the scan limit.
var ErrChartTooLarge = errors.New("chart exceeds max unpacked size")

// cappedReader fails with ErrChartTooLarge once n bytes have been read.
type cappedReader struct {
	r io.Reader
	n int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.n <= 0 {
		return 0, ErrChartTooLarge
	}

	if int64(len(p)) > c.n {
		p = p[:c.n]
	}

	n, err := c.r.Read(p)
	c.n -= int64(n)

	return n, err
}

// ChartMetadata represents the metadata from Chart.yaml.
type ChartMetadata struct {
	APIVersion  string            `yaml:"apiVersion"`
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	AppVersion  string            `yaml:"appVersion"`
	Description string            `yaml:"description"`
	Type        string            `yaml:"type"`
	Keywords    []string          `yaml:"keywords"`
	Home        string            `yaml:"home"`
	Sources     []string          `yaml:"sources"`
	Maintainers []map[string]any  `yaml:"maintainers"`
	Icon        string            `yaml:"icon"`
	Annotations map[string]string `yaml:"annotations"`
}

// ParseChart extracts metadata from a Helm chart tarball.
func ParseChart(r io.Reader) (map[string]any, error) {
	// Decompress gzip
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read gzip: %w", err)
	}
	defer func() { _ = gr.Close() }()

	// Read tar archive
	tr := tar.NewReader(&cappedReader{r: gr, n: maxUnpackedChartSize})

	var metadata *ChartMetadata

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read tar: %w", err)
		}

		// Only the chart root's Chart.yaml (<chart>/Chart.yaml) counts; nested
		// subchart manifests (<chart>/charts/<sub>/Chart.yaml) must not win.
		if header.Typeflag == tar.TypeReg && isRootChartYAML(header.Name) {
			data, err := readAllLimited(tr, maxChartYAMLSize, ErrChartYAMLTooLarge)
			if err != nil {
				return nil, fmt.Errorf("failed to read Chart.yaml: %w", err)
			}

			metadata = &ChartMetadata{}
			if err := yaml.Unmarshal(data, metadata); err != nil {
				return nil, fmt.Errorf("failed to parse Chart.yaml: %w", err)
			}

			break
		}
	}

	if metadata == nil {
		return nil, ErrChartYAMLNotFound
	}

	// Convert to map[string]interface{}
	result := map[string]any{
		"apiVersion":  metadata.APIVersion,
		"name":        metadata.Name,
		"version":     metadata.Version,
		"appVersion":  metadata.AppVersion,
		"description": metadata.Description,
		"type":        metadata.Type,
		"keywords":    metadata.Keywords,
		"home":        metadata.Home,
		"sources":     metadata.Sources,
		"maintainers": metadata.Maintainers,
		"icon":        metadata.Icon,
		"annotations": metadata.Annotations,
	}

	return result, nil
}

// isRootChartYAML reports whether a tar member name is <chart>/Chart.yaml. The
// directory must be a relative one: helm refuses archives with absolute paths,
// so "/Chart.yaml" (or "/../Chart.yaml", which cleans to it) is not a chart's
// manifest.
func isRootChartYAML(name string) bool {
	name = path.Clean(strings.TrimPrefix(name, "./"))
	parts := strings.Split(name, "/")

	return len(parts) == 2 && pathseg.Name(parts[0]) && parts[1] == "Chart.yaml"
}
