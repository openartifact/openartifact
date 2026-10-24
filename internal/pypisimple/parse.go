// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypisimple

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"strings"
)

// IsJSON reports whether a response Content-Type is the PEP 691 JSON
// serialization (anything else is parsed as HTML).
func IsJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}

	return strings.HasSuffix(mt, "+json") || mt == "application/json"
}

// ParseProject parses a project page in the serialization named by contentType.
func ParseProject(body []byte, contentType string) ([]File, error) {
	if IsJSON(contentType) {
		return ParseProjectJSON(body)
	}

	return ParseProjectHTML(bytes.NewReader(body))
}

// ParseIndex parses the project index in the serialization named by
// contentType and returns the PEP 503 normalized names, in listed order and
// without duplicates. Names that cannot be a safe path segment are dropped.
func ParseIndex(body []byte, contentType string) ([]string, error) {
	var (
		names []string
		err   error
	)

	if IsJSON(contentType) {
		names, err = ParseIndexJSON(body)
	} else {
		names, err = ParseIndexHTML(bytes.NewReader(body))
	}

	if err != nil {
		return nil, err
	}

	norm := names[:0]

	for _, n := range names {
		if n = NormalizeName(n); ValidName(n) {
			norm = append(norm, n)
		}
	}

	return MergeNames(norm), nil
}

type jsonFile struct {
	Filename         string            `json:"filename"`
	URL              string            `json:"url"`
	Hashes           map[string]string `json:"hashes"`
	RequiresPython   *string           `json:"requires-python"`    //nolint:tagliatelle // PEP 691 wire format
	CoreMetadata     json.RawMessage   `json:"core-metadata"`      //nolint:tagliatelle // PEP 691 wire format
	DistInfoMetadata json.RawMessage   `json:"dist-info-metadata"` //nolint:tagliatelle // PEP 691 wire format
	Yanked           json.RawMessage   `json:"yanked"`
	Size             *int64            `json:"size"`
	UploadTime       string            `json:"upload-time"` //nolint:tagliatelle // PEP 691 wire format
}

type jsonProject struct {
	Files []jsonFile `json:"files"`
}

type jsonIndex struct {
	Projects []struct {
		Name string `json:"name"`
	} `json:"projects"`
}

// ParseProjectJSON returns the files of a PEP 691 project document.
func ParseProjectJSON(body []byte) ([]File, error) {
	var doc jsonProject
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse simple project json: %w", err)
	}

	files := make([]File, 0, len(doc.Files))

	for i := range doc.Files {
		jf := &doc.Files[i]

		// A link that is only a fragment names no file: resolved against the
		// page it would be the page itself.
		u := stripFragment(jf.URL)
		if jf.Filename == "" || u == "" {
			continue
		}

		f := File{Filename: jf.Filename, URL: u, Hashes: lowerHashes(jf.Hashes), UploadTime: jf.UploadTime}

		if jf.RequiresPython != nil {
			f.RequiresPython = *jf.RequiresPython
		}

		if jf.Size != nil && *jf.Size > 0 {
			f.Size = *jf.Size
		}

		hint := jf.CoreMetadata
		if len(hint) == 0 {
			hint = jf.DistInfoMetadata
		}

		f.CoreMetadata, f.CoreMetadataHashes = parseJSONMetadataHint(hint)
		f.Yanked, f.YankedReason = parseJSONYanked(jf.Yanked)

		files = append(files, f)
	}

	return files, nil
}

// ParseIndexJSON returns the project names of a PEP 691 index document.
func ParseIndexJSON(body []byte) ([]string, error) {
	var doc jsonIndex
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse simple index json: %w", err)
	}

	names := make([]string, 0, len(doc.Projects))

	for _, p := range doc.Projects {
		if p.Name != "" {
			names = append(names, p.Name)
		}
	}

	return names, nil
}

func stripFragment(u string) string {
	u, _, _ = strings.Cut(u, "#")

	return u
}

func lowerHashes(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]string, len(in))

	for k, v := range in {
		// Spellings that differ only in case name one algorithm and map
		// iteration order is random: on a conflict keep the greater digest so
		// the result does not depend on it.
		k, v = strings.ToLower(k), strings.ToLower(v)
		if prev, dup := out[k]; !dup || v > prev {
			out[k] = v
		}
	}

	return out
}

// parseJSONMetadataHint reads core-metadata: a bool or a hashes object.
func parseJSONMetadataHint(raw json.RawMessage) (bool, map[string]string) {
	if len(raw) == 0 {
		return false, nil
	}

	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, nil
	}

	var hashes map[string]string
	if json.Unmarshal(raw, &hashes) == nil {
		return true, lowerHashes(hashes)
	}

	return false, nil
}

// parseJSONYanked reads yanked: false, true or a reason string.
func parseJSONYanked(raw json.RawMessage) (bool, string) {
	if len(raw) == 0 {
		return false, ""
	}

	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, ""
	}

	var reason string
	if json.Unmarshal(raw, &reason) == nil {
		return true, reason
	}

	return false, ""
}
