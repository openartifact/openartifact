// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"go.yaml.in/yaml/v3"
)

// helmIndexEntryBytes is the rendered size assumed per chart when sizing the
// index.yaml buffer.
const helmIndexEntryBytes = 768

// errKeyOrder reports that the yaml library did not return every chart name.
var errKeyOrder = errors.New("order index entries")

// helmIndexHeader is index.yaml up to its entries.
type helmIndexHeader struct {
	APIVersion string `yaml:"apiVersion"`
	Generated  string `yaml:"generated"`
}

// helmIndexEntries is the entries mapping of index.yaml, holding as many chart
// names as are marshalled together.
type helmIndexEntries struct {
	Entries map[string][]HelmChartEntry `yaml:"entries"`
}

// HelmChartEntry is one chart version in index.yaml.
type HelmChartEntry struct {
	Name        string              `yaml:"name"`
	Version     string              `yaml:"version"`
	AppVersion  string              `yaml:"appVersion,omitempty"`
	Description string              `yaml:"description,omitempty"`
	Digest      string              `yaml:"digest"`
	URLs        []string            `yaml:"urls"`
	Created     string              `yaml:"created"`
	Home        string              `yaml:"home,omitempty"`
	Sources     []string            `yaml:"sources,omitempty"`
	Maintainers []map[string]string `yaml:"maintainers,omitempty"`
	Icon        string              `yaml:"icon,omitempty"`
	APIVersion  string              `yaml:"apiVersion,omitempty"`
	Type        string              `yaml:"type,omitempty"`
	Keywords    []string            `yaml:"keywords,omitempty"`
}

func newHelmChartEntry(chart *models.HelmChart) HelmChartEntry {
	return HelmChartEntry{
		Name:        chart.Name,
		Version:     chart.Version,
		AppVersion:  chart.AppVersion,
		Description: chart.Description,
		Digest:      chart.Digest,
		URLs:        []string{"charts/" + url.PathEscape(chart.Name+"-"+chart.Version) + ".tgz"},
		Created:     chart.CreatedAt.UTC().Format(time.RFC3339),
		Home:        chart.Home,
		Sources:     chart.Sources,
		Maintainers: chart.Maintainers,
		Icon:        chart.Icon,
		APIVersion:  chart.APIVersion,
		Type:        chart.Type,
		Keywords:    chart.Keywords,
	}
}

// renderHelmIndex returns index.yaml for charts, generated at the given time.
//
// The document is the one yaml.Marshal makes of {apiVersion, generated,
// entries}, byte for byte, but it is produced one chart name at a time:
// go.yaml.in/yaml/v3 keeps every event of a document in memory until the
// document ends, which cost 700 MB to render 10000 charts. The entries of a
// name are written directly when all their strings are plain scalars
// (writeGroupDirect), which is what charts normally are; otherwise the name is
// marshalled as its own `entries:` mapping, whose first line is dropped, so
// the indentation (and with it the line folding) is the one of the whole
// document.
func renderHelmIndex(charts []*models.HelmChart, generated time.Time) ([]byte, error) {
	var buf bytes.Buffer

	buf.Grow(len(charts) * helmIndexEntryBytes)

	header, err := yaml.Marshal(helmIndexHeader{APIVersion: "v1", Generated: generated.UTC().Format(time.RFC3339)})
	if err != nil {
		return nil, fmt.Errorf("marshal index header: %w", err)
	}

	buf.Write(header)

	if len(charts) == 0 {
		buf.WriteString("entries: {}\n")

		return buf.Bytes(), nil
	}

	byName := make(map[string][]*models.HelmChart)
	for _, chart := range charts {
		byName[chart.Name] = append(byName[chart.Name], chart)
	}

	names, err := yamlKeyOrder(byName)
	if err != nil {
		return nil, err
	}

	const entriesLine = "entries:\n"

	buf.WriteString(entriesLine)

	group := helmIndexEntries{Entries: make(map[string][]HelmChartEntry, 1)}

	var entries []HelmChartEntry

	for _, name := range names {
		if writeGroupDirect(&buf, name, byName[name]) {
			continue
		}

		entries = entries[:0]
		for _, chart := range byName[name] {
			entries = append(entries, newHelmChartEntry(chart))
		}

		clear(group.Entries)
		group.Entries[name] = entries

		out, err := yaml.Marshal(group)
		if err != nil {
			return nil, fmt.Errorf("marshal index entries of %q: %w", name, err)
		}

		buf.Write(bytes.TrimPrefix(out, []byte(entriesLine)))
	}

	return buf.Bytes(), nil
}

// yamlKeyOrder returns the keys of m in the order yaml.Marshal writes a map
// with them, so the library stays the one definition of that order. It
// encodes the keys alone.
func yamlKeyOrder(m map[string][]*models.HelmChart) ([]string, error) {
	keys := make(map[string]struct{}, len(m))
	for k := range m {
		keys[k] = struct{}{}
	}

	var node yaml.Node
	if err := node.Encode(keys); err != nil {
		return nil, fmt.Errorf("order index entries: %w", err)
	}

	names := make([]string, 0, len(keys))
	for i := 0; i+1 < len(node.Content); i += 2 {
		names = append(names, node.Content[i].Value)
	}

	if len(names) != len(keys) {
		return nil, fmt.Errorf("%w: %d of %d names", errKeyOrder, len(names), len(keys))
	}

	return names, nil
}
