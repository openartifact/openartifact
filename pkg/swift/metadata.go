// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	// MaxMetadataBytes is the largest metadata document of a release.
	MaxMetadataBytes = 1 << 20
	// MaxRepositoryURLs is the most repository URLs a release may declare.
	MaxRepositoryURLs = 64
	// MaxURLLen is the longest URL in the metadata.
	MaxURLLen = 2048
)

// ErrInvalidMetadata is wrapped by every error of ParseMetadata.
var ErrInvalidMetadata = errors.New("invalid package metadata")

// Metadata is the metadata a release is published with (the "metadata" part of
// the publish request). The server keeps the document verbatim; these are the
// fields it reads.
type Metadata struct {
	Description             string   `json:"description"`
	LicenseURL              string   `json:"licenseURL"`              //nolint:tagliatelle // package-metadata.json is camelCase (SE-0391)
	OriginalPublicationTime string   `json:"originalPublicationTime"` //nolint:tagliatelle // package-metadata.json is camelCase (SE-0391)
	ReadmeURL               string   `json:"readmeURL"`               //nolint:tagliatelle // package-metadata.json is camelCase (SE-0391)
	RepositoryURLs          []string `json:"repositoryURLs"`          //nolint:tagliatelle // package-metadata.json is camelCase (SE-0391)
}

// ParseMetadata validates the metadata document of a release and returns its
// compact form with the fields read from it. The document must be a JSON
// object; licenseURL and readmeURL must be absolute http(s) URLs,
// originalPublicationTime an RFC 3339 time and repositoryURLs a bounded list of
// non-empty strings. Other fields are kept as sent.
func ParseMetadata(data []byte) (json.RawMessage, Metadata, error) {
	var m Metadata

	if len(data) > MaxMetadataBytes {
		return nil, m, fmt.Errorf("%w: larger than %d bytes", ErrInvalidMetadata, MaxMetadataBytes)
	}

	if t := bytes.TrimSpace(data); len(t) == 0 || t[0] != '{' {
		return nil, m, fmt.Errorf("%w: not a JSON object", ErrInvalidMetadata)
	}

	if err := json.Unmarshal(data, &m); err != nil {
		return nil, m, fmt.Errorf("%w: %w", ErrInvalidMetadata, err)
	}

	for field, u := range map[string]string{"licenseURL": m.LicenseURL, "readmeURL": m.ReadmeURL} {
		if u != "" && !absoluteHTTP(u) {
			return nil, m, fmt.Errorf("%w: %s is not an absolute http(s) URL", ErrInvalidMetadata, field)
		}
	}

	if m.OriginalPublicationTime != "" {
		if _, err := time.Parse(time.RFC3339, m.OriginalPublicationTime); err != nil {
			return nil, m, fmt.Errorf("%w: originalPublicationTime is not an RFC 3339 time", ErrInvalidMetadata)
		}
	}

	if len(m.RepositoryURLs) > MaxRepositoryURLs {
		return nil, m, fmt.Errorf("%w: more than %d repositoryURLs", ErrInvalidMetadata, MaxRepositoryURLs)
	}

	for _, u := range m.RepositoryURLs {
		if strings.TrimSpace(u) == "" || len(u) > MaxURLLen {
			return nil, m, fmt.Errorf("%w: invalid repositoryURLs entry", ErrInvalidMetadata)
		}
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, m, fmt.Errorf("%w: %w", ErrInvalidMetadata, err)
	}

	return compact.Bytes(), m, nil
}

func absoluteHTTP(s string) bool {
	if len(s) > MaxURLLen {
		return false
	}

	u, err := url.Parse(s)

	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// NormalizeURL reduces a repository URL to the form two spellings of the same
// repository share, so "https://github.com/mona/LinkedList.git",
// "git@github.com:mona/LinkedList" and "ssh://git@github.com/mona/linkedlist/"
// all become "github.com/mona/linkedlist": no scheme, user or port, no leading
// "www.", no query, trailing slash or ".git", lower case. A string with no
// host or no path returns "".
func NormalizeURL(s string) string {
	s = strings.TrimSpace(s)

	if rest, ok := scpForm(s); ok {
		s = rest
	} else if _, after, found := strings.Cut(s, "://"); found {
		s = after
	}

	authority, path, _ := strings.Cut(s, "/")

	// Drop the user (and password) and the port of the authority.
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		authority = authority[i+1:]
	}

	host, _, _ := strings.Cut(authority, ":")

	host = strings.ToLower(strings.TrimSpace(host))
	for strings.HasPrefix(host, "www.") {
		host = strings.TrimPrefix(host, "www.")
	}

	// A query or fragment does not name a repository.
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}

	path = strings.ToLower(strings.TrimSpace(path))

	for {
		trimmed := strings.TrimSuffix(strings.TrimFunc(path, slashOrSpace), ".git")
		if trimmed == path {
			break
		}

		path = trimmed
	}

	// A second "://" would be taken for the scheme on the next pass.
	if host == "" || path == "" || strings.ContainsFunc(host, unicode.IsSpace) || strings.Contains(path, "://") {
		return ""
	}

	return host + "/" + path
}

func slashOrSpace(r rune) bool { return r == '/' || unicode.IsSpace(r) }

// scpForm rewrites the scp-like "user@host:path" to "user@host/path". It only
// applies to a string with no scheme whose colon precedes any slash.
func scpForm(s string) (string, bool) {
	if strings.Contains(s, "://") {
		return "", false
	}

	colon, slash := strings.IndexByte(s, ':'), strings.IndexByte(s, '/')
	if colon <= 0 || (slash >= 0 && slash < colon) {
		return "", false
	}

	return s[:colon] + "/" + strings.TrimLeft(s[colon+1:], "/"), true
}
