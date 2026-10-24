// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package base

import (
	"encoding/xml"
	"path"
	"strings"
)

// maxLicenseXMLBytes bounds the descriptors inspected for declared licenses.
const maxLicenseXMLBytes = 1 << 20

// SpoolDescriptorLicenses is DescriptorLicenses for an uploaded body: only a
// descriptor small enough to be parsed is read back from the spool.
func SpoolDescriptorLicenses(filename string, s *Spool) []string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".xml", ".pom":
	default:
		return nil
	}

	if s.Size() > maxLicenseXMLBytes {
		return nil
	}

	data, err := s.Bytes()
	if err != nil {
		return nil
	}

	return DescriptorLicenses(filename, data)
}

// DescriptorLicenses returns the licenses declared by a published Ivy module
// descriptor (<info><license name="..."/></info>) or Maven POM
// (<licenses><license><name>). Files that are not a .xml/.pom descriptor, are
// oversized or cannot be parsed declare no license.
func DescriptorLicenses(filename string, data []byte) []string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".xml", ".pom":
	default:
		return nil
	}

	if len(data) > maxLicenseXMLBytes {
		return nil
	}

	var doc struct {
		Info struct {
			Licenses []struct {
				Name string `xml:"name,attr"`
			} `xml:"license"`
		} `xml:"info"`
		Licenses struct {
			License []struct {
				Name string `xml:"name"`
			} `xml:"license"`
		} `xml:"licenses"`
	}

	if xml.Unmarshal(data, &doc) != nil {
		return nil
	}

	var out []string

	for _, l := range doc.Info.Licenses {
		if n := strings.TrimSpace(l.Name); n != "" {
			out = append(out, n)
		}
	}

	for _, l := range doc.Licenses.License {
		if n := strings.TrimSpace(l.Name); n != "" {
			out = append(out, n)
		}
	}

	return out
}
