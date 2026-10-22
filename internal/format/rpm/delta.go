// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"time"
)

type DeltaRPM struct {
	NewName      string
	NewVer       string
	NewRel       string
	NewEpoch     uint32
	NewArch      string
	OldName      string
	OldVer       string
	OldRel       string
	OldEpoch     uint32
	Filename     string
	Size         int64
	Checksum     string
	ChecksumType string // e.g. "sha256"
}

type PrestoDeltaXML struct {
	XMLName xml.Name     `xml:"prestodelta"`
	Deltas  []DeltaEntry `xml:"newpackage"`
}

type DeltaEntry struct {
	XMLName xml.Name      `xml:"newpackage"`
	Name    string        `xml:"name,attr"`
	Arch    string        `xml:"arch,attr"`
	Ver     string        `xml:"ver,attr"`
	Rel     string        `xml:"rel,attr"`
	Epoch   string        `xml:"epoch,attr,omitempty"`
	Deltas  []DeltaDetail `xml:"delta"`
}

type DeltaDetail struct {
	XMLName  xml.Name      `xml:"delta"`
	OldEpoch string        `xml:"oldepoch,attr,omitempty"`
	OldVer   string        `xml:"oldver,attr"`
	OldRel   string        `xml:"oldrel,attr"`
	Filename string        `xml:"filename"`
	Size     int64         `xml:"size"`
	Checksum DeltaChecksum `xml:"checksum"`
}

// DeltaChecksum is <checksum type="sha256">hex</checksum>.
type DeltaChecksum struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

func GeneratePrestodeltaXML(packages []DeltaRPM) ([]byte, error) {
	if len(packages) == 0 {
		return nil, nil
	}

	deltas := make(map[string][]DeltaDetail)

	for _, pkg := range packages {
		entry := DeltaDetail{
			OldEpoch: strconv.FormatUint(uint64(pkg.OldEpoch), 10),
			OldVer:   pkg.OldVer,
			OldRel:   pkg.OldRel,
			Filename: pkg.Filename,
			Size:     pkg.Size,
			Checksum: DeltaChecksum{Type: pkg.ChecksumType, Value: pkg.Checksum},
		}
		key := fmt.Sprintf("%s-%s-%s", pkg.NewName, pkg.NewArch, pkg.NewVer)
		deltas[key] = append(deltas[key], entry)
	}

	var entries []DeltaEntry

	seen := make(map[string]bool)

	for _, pkg := range packages {
		key := fmt.Sprintf("%s-%s-%s", pkg.NewName, pkg.NewArch, pkg.NewVer)
		if seen[key] {
			continue
		}

		seen[key] = true

		epochStr := ""
		if pkg.NewEpoch > 0 {
			epochStr = strconv.FormatUint(uint64(pkg.NewEpoch), 10)
		}

		entries = append(entries, DeltaEntry{
			Name:   pkg.NewName,
			Arch:   pkg.NewArch,
			Ver:    pkg.NewVer,
			Rel:    pkg.NewRel,
			Epoch:  epochStr,
			Deltas: deltas[key],
		})
	}

	presto := PrestoDeltaXML{Deltas: entries}

	output, err := xml.MarshalIndent(presto, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rpm: marshaling prestodelta XML: %w", err)
	}

	result := append([]byte(xml.Header), output...)

	return result, nil
}

func FormatTimestamp(ts int64) string {
	return time.Unix(ts, 0).UTC().Format("2006-01-02T15:04:05Z")
}
