// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"encoding/xml"
	"fmt"
	"time"

	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

type SnapshotEntry struct {
	Extension  string
	Classifier string
	Value      string
	Updated    string
}

type groupMetadataXML struct {
	XMLName    xml.Name           `xml:"metadata"`
	GroupID    string             `xml:"groupId"`
	ArtifactID string             `xml:"artifactId"`
	Versioning groupVersioningXML `xml:"versioning"`
}

type groupVersioningXML struct {
	Latest      string           `xml:"latest"`
	Release     string           `xml:"release"`
	Versions    groupVersionsXML `xml:"versions"`
	LastUpdated string           `xml:"lastUpdated"`
}

type groupVersionsXML struct {
	Version []string `xml:"version"`
}

type snapshotMetadataXML struct {
	XMLName    xml.Name              `xml:"metadata"`
	GroupID    string                `xml:"groupId"`
	ArtifactID string                `xml:"artifactId"`
	Version    string                `xml:"version"`
	Versioning snapshotVersioningXML `xml:"versioning"`
}

type snapshotVersioningXML struct {
	Snapshot         snapshotXML          `xml:"snapshot"`
	LastUpdated      string               `xml:"lastUpdated"`
	SnapshotVersions []snapshotVersionXML `xml:"snapshotVersions>snapshotVersion"`
}

type snapshotXML struct {
	Timestamp   string `xml:"timestamp"`
	BuildNumber int    `xml:"buildNumber"`
}

type snapshotVersionXML struct {
	Classifier string `xml:"classifier,omitempty"`
	Extension  string `xml:"extension"`
	Value      string `xml:"value"`
	Updated    string `xml:"updated"`
}

func GenerateGroupMetadataXML(
	groupID, artifactID string,
	versions []string,
	latestRelease, latestVersion string,
	lastUpdated time.Time,
) ([]byte, error) {
	metadata := groupMetadataXML{
		GroupID:    groupID,
		ArtifactID: artifactID,
		Versioning: groupVersioningXML{
			Latest:      latestVersion,
			Release:     latestRelease,
			Versions:    groupVersionsXML{Version: versions},
			LastUpdated: lastUpdated.UTC().Format("20060102150405"),
		},
	}

	output, err := xml.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("maven: marshaling group metadata xml: %w", err)
	}

	return append([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"), output...), nil
}

func GenerateSnapshotMetadataXML(
	coord pkgmaven.Coordinate,
	snapshotTimestamp string,
	snapshotBuildNumber int,
	lastUpdated time.Time,
	entries []SnapshotEntry,
) ([]byte, error) {
	snapshotVersions := make([]snapshotVersionXML, len(entries))
	for i, e := range entries {
		snapshotVersions[i] = snapshotVersionXML{
			Classifier: e.Classifier,
			Extension:  e.Extension,
			Value:      e.Value,
			Updated:    e.Updated,
		}
	}

	metadata := snapshotMetadataXML{
		GroupID:    coord.GroupID,
		ArtifactID: coord.ArtifactID,
		Version:    coord.Version,
		Versioning: snapshotVersioningXML{
			Snapshot: snapshotXML{
				Timestamp:   snapshotTimestamp,
				BuildNumber: snapshotBuildNumber,
			},
			LastUpdated:      lastUpdated.UTC().Format("20060102150405"),
			SnapshotVersions: snapshotVersions,
		},
	}

	output, err := xml.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("maven: marshaling snapshot metadata xml: %w", err)
	}

	return append([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"), output...), nil
}
