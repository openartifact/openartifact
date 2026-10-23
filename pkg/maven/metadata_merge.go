// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var (
	// ErrNoMetadata is returned when MergeMetadata is given nothing to merge.
	ErrNoMetadata = errors.New("maven: no metadata documents to merge")

	errNotMetadata = errors.New("maven: not a maven-metadata document")
)

// Metadata is a maven-metadata.xml document at any of its three levels:
// group (plugin prefixes), artifact (version list) or version (snapshot
// resolution).
type Metadata struct {
	XMLName    xml.Name    `xml:"metadata"`
	GroupID    string      `xml:"groupId,omitempty"`
	ArtifactID string      `xml:"artifactId,omitempty"`
	Version    string      `xml:"version,omitempty"`
	Versioning *Versioning `xml:"versioning,omitempty"`
	Plugins    []Plugin    `xml:"plugins>plugin,omitempty"`
}

// Versioning is the <versioning> element of a Metadata document.
type Versioning struct {
	Latest           string            `xml:"latest,omitempty"`
	Release          string            `xml:"release,omitempty"`
	Versions         []string          `xml:"versions>version,omitempty"`
	LastUpdated      string            `xml:"lastUpdated,omitempty"`
	Snapshot         *Snapshot         `xml:"snapshot,omitempty"`
	SnapshotVersions []SnapshotVersion `xml:"snapshotVersions>snapshotVersion,omitempty"`
}

// Snapshot identifies the latest deployed snapshot build.
type Snapshot struct {
	Timestamp   string `xml:"timestamp,omitempty"`
	BuildNumber int    `xml:"buildNumber,omitempty"`
	LocalCopy   bool   `xml:"localCopy,omitempty"`
}

// SnapshotVersion maps one snapshot file kind to its timestamped version.
type SnapshotVersion struct {
	Classifier string `xml:"classifier,omitempty"`
	Extension  string `xml:"extension"`
	Value      string `xml:"value"`
	Updated    string `xml:"updated,omitempty"`
}

// Plugin is one plugin-prefix mapping of a group-level document.
type Plugin struct {
	Name       string `xml:"name,omitempty"`
	Prefix     string `xml:"prefix"`
	ArtifactID string `xml:"artifactId"`
}

// ParseMetadata decodes a maven-metadata.xml document.
func ParseMetadata(data []byte) (*Metadata, error) {
	var m Metadata
	if err := xml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("maven: parsing metadata: %w", err)
	}

	if m.XMLName.Local != "metadata" {
		return nil, errNotMetadata
	}

	return &m, nil
}

// Marshal renders the document with an XML declaration.
func (m *Metadata) Marshal() ([]byte, error) {
	out, err := xml.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("maven: marshaling metadata: %w", err)
	}

	return append([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"), append(out, '\n')...), nil
}

// MergeMetadata merges the maven-metadata.xml documents of several
// repositories serving the same path, the way a group repository exposes its
// members:
//
//   - artifact level: the version lists are united and ordered by Maven version
//     order; latest is the highest version, release the highest non-SNAPSHOT
//     version, lastUpdated the newest of the inputs;
//   - group level: plugin prefix mappings are united (first member wins per
//     prefix);
//   - version (snapshot) level: the document with the newest lastUpdated wins
//     as a whole, since snapshot builds of different members cannot be mixed.
func MergeMetadata(docs ...*Metadata) (*Metadata, error) {
	if len(docs) == 0 {
		return nil, ErrNoMetadata
	}

	merged := &Metadata{XMLName: docs[0].XMLName}

	for _, d := range docs {
		if merged.GroupID == "" {
			merged.GroupID = d.GroupID
		}

		if merged.ArtifactID == "" {
			merged.ArtifactID = d.ArtifactID
		}

		if merged.Version == "" {
			merged.Version = d.Version
		}
	}

	if merged.Version != "" {
		return newestSnapshotMetadata(docs), nil
	}

	merged.Plugins = mergePlugins(docs)
	merged.Versioning = mergeVersioning(docs)

	return merged, nil
}

func newestSnapshotMetadata(docs []*Metadata) *Metadata {
	best := docs[0]

	for _, d := range docs[1:] {
		if lastUpdated(d) > lastUpdated(best) {
			best = d
		}
	}

	return best
}

func lastUpdated(m *Metadata) string {
	if m.Versioning == nil {
		return ""
	}

	return m.Versioning.LastUpdated
}

func mergePlugins(docs []*Metadata) []Plugin {
	var plugins []Plugin

	seen := map[string]bool{}

	for _, d := range docs {
		for _, p := range d.Plugins {
			if seen[p.Prefix] {
				continue
			}

			seen[p.Prefix] = true

			plugins = append(plugins, p)
		}
	}

	return plugins
}

func mergeVersioning(docs []*Metadata) *Versioning {
	var (
		versions []string
		updated  string
		found    bool
	)

	seen := map[string]bool{}

	for _, d := range docs {
		v := d.Versioning
		if v == nil {
			continue
		}

		found = true

		if v.LastUpdated > updated {
			updated = v.LastUpdated
		}

		for _, ver := range v.Versions {
			ver = strings.TrimSpace(ver)
			if ver == "" || seen[ver] {
				continue
			}

			seen[ver] = true

			versions = append(versions, ver)
		}
	}

	if !found {
		return nil
	}

	SortVersions(versions)

	out := &Versioning{Versions: versions, LastUpdated: updated}
	if len(versions) > 0 {
		out.Latest = versions[len(versions)-1]
	}

	for _, v := range slices.Backward(versions) {
		if !strings.HasSuffix(v, "-SNAPSHOT") {
			out.Release = v
			break
		}
	}

	return out
}
