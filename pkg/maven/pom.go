// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"encoding/xml"
	"io"
)

type License struct {
	Name string `xml:"name"`
	URL  string `xml:"url"`
}

type Dependency struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Scope      string `xml:"scope"`
	Type       string `xml:"type"`
	Classifier string `xml:"classifier"`
	Optional   string `xml:"optional"`
}

type POM struct {
	XMLName      xml.Name     `xml:"project"`
	GroupID      string       `xml:"groupId"`
	ArtifactID   string       `xml:"artifactId"`
	Version      string       `xml:"version"`
	Packaging    string       `xml:"packaging"`
	Name         string       `xml:"name"`
	Description  string       `xml:"description"`
	URL          string       `xml:"url"`
	Licenses     []License    `xml:"licenses>license"`
	Dependencies []Dependency `xml:"dependencies>dependency"`
}

// MaxPOMBytes bounds the pom bytes read: element text accumulates in memory,
// so an unbounded document could exhaust it. Uploads of larger poms are refused.
const MaxPOMBytes = 4 << 20

func ParsePOM(r io.Reader) (*POM, error) {
	var pom POM
	if err := xml.NewDecoder(io.LimitReader(r, MaxPOMBytes)).Decode(&pom); err != nil {
		return nil, err
	}

	return &pom, nil
}
