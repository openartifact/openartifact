// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cargo holds the parts of the Cargo sparse registry protocol that
// proxy and group repositories need: request-path parsing, index file
// validation and merging, the registry config.json and its download template.
package cargo

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// MaxIndexBytes bounds one index file read into memory. The largest crates
// have a few thousand versions, a few MiB of index.
const MaxIndexBytes = 64 << 20

// Kind is the type of a registry request path.
type Kind int

const (
	// KindConfig is config.json (at the registry root or below index/).
	KindConfig Kind = iota
	// KindIndex is a sparse index file: index/<prefix>/<crate>.
	KindIndex
	// KindDownload is api/v1/crates/<crate>/<version>/download.
	KindDownload
)

// Path is a parsed registry request path.
type Path struct {
	Kind Kind
	// Crate is the crate name; lowercase for KindIndex.
	Crate string
	// Version is set for KindDownload.
	Version string
	// IndexPath is the path of a KindIndex file below index/, e.g. "se/rd/serde".
	IndexPath string
}

// ErrBadPath is returned for a path that is not a registry request.
var ErrBadPath = errors.New("not a cargo registry path")

const maxNameLen = 64

var (
	indexNameRE    = regexp.MustCompile(`^[a-z0-9_-]+$`)
	downloadNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	versionRE      = regexp.MustCompile(`^[0-9A-Za-z.+-]+$`)
	cksumRE        = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// IndexPathFor returns the path of crate's index file below the index root:
// "1/<n>", "2/<n>", "3/<c>/<n>" or "<ab>/<cd>/<n>". name must be ASCII.
func IndexPathFor(name string) string {
	switch len(name) {
	case 0:
		return ""
	case 1, 2:
		return fmt.Sprintf("%d/%s", len(name), name)
	case 3:
		return "3/" + name[:1] + "/" + name
	default:
		return name[:2] + "/" + name[2:4] + "/" + name
	}
}

// ParsePath classifies relPath, the path below the repository root. Index
// paths must use the canonical lowercase layout cargo requests.
func ParsePath(relPath string) (Path, error) {
	relPath = strings.TrimPrefix(relPath, "/")

	if relPath == "config.json" || relPath == "index/config.json" {
		return Path{Kind: KindConfig}, nil
	}

	if rest, ok := strings.CutPrefix(relPath, "index/"); ok {
		return parseIndexPath(rest)
	}

	if rest, ok := strings.CutPrefix(relPath, "api/v1/crates/"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) != 3 || parts[2] != "download" {
			return Path{}, ErrBadPath
		}

		crate, version := parts[0], parts[1]
		if len(crate) > maxNameLen || !downloadNameRE.MatchString(crate) || len(version) > 100 || !versionRE.MatchString(version) {
			return Path{}, ErrBadPath
		}

		return Path{Kind: KindDownload, Crate: crate, Version: version}, nil
	}

	return Path{}, ErrBadPath
}

func parseIndexPath(rest string) (Path, error) {
	parts := strings.Split(rest, "/")
	name := parts[len(parts)-1]

	if len(name) > maxNameLen || !indexNameRE.MatchString(name) || IndexPathFor(name) != rest {
		return Path{}, ErrBadPath
	}

	return Path{Kind: KindIndex, Crate: name, IndexPath: rest}, nil
}

// Entry is one line of an index file.
type Entry struct {
	Vers  string
	Cksum string
	// Line is the raw JSON of the entry, without the newline.
	Line []byte
}

var errBadIndex = errors.New("not a cargo index file")

// ParseIndex parses the newline-delimited JSON of an index file. Every
// non-blank line must be a JSON object with string "name" and "vers"; an
// index file without entries is refused.
func ParseIndex(doc []byte) ([]Entry, error) {
	var entries []Entry

	sc := bufio.NewScanner(bytes.NewReader(doc))
	sc.Buffer(make([]byte, 0, 64<<10), MaxIndexBytes)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var e struct {
			Name  *string `json:"name"`
			Vers  *string `json:"vers"`
			Cksum string  `json:"cksum"`
		}

		if err := json.Unmarshal(line, &e); err != nil || e.Name == nil || e.Vers == nil {
			return nil, errBadIndex
		}

		entries = append(entries, Entry{Vers: *e.Vers, Cksum: e.Cksum, Line: bytes.Clone(line)})
	}

	if err := sc.Err(); err != nil || len(entries) == 0 {
		return nil, errBadIndex
	}

	return entries, nil
}

// VerifyIndex reports whether doc, read from spool, is a well-formed index
// file; it is the integrity check applied to an upstream answer before it is
// cached.
func VerifyIndex(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	doc, err := io.ReadAll(io.LimitReader(spool, MaxIndexBytes+1))
	if err != nil || len(doc) > MaxIndexBytes {
		return errBadIndex
	}

	_, err = ParseIndex(doc)

	return err
}

// VerifyConfig reports whether spool holds a registry config.json: a JSON
// object with a string "dl".
func VerifyConfig(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	if _, err := ParseConfig(io.LimitReader(spool, 1<<20)); err != nil {
		return err
	}

	return nil
}

// Config is the part of a registry config.json this package reads.
type Config struct {
	DL string `json:"dl"`
}

var errBadConfig = errors.New("not a cargo registry config")

// ParseConfig reads a registry config.json.
func ParseConfig(r io.Reader) (Config, error) {
	var c Config
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&c); err != nil || c.DL == "" {
		return Config{}, errBadConfig
	}

	return c, nil
}

// FindVersion returns the entry of index doc whose version is exactly vers.
func FindVersion(doc []byte, vers string) (Entry, bool, error) {
	entries, err := ParseIndex(doc)
	if err != nil {
		return Entry{}, false, err
	}

	for _, e := range entries {
		if e.Vers == vers {
			return e, true, nil
		}
	}

	return Entry{}, false, nil
}

// ValidCksum reports whether s is a hex sha256 digest.
func ValidCksum(s string) bool { return cksumRE.MatchString(s) }

// DownloadURL expands a registry's "dl" template for one crate version as
// cargo does: the markers {crate}, {version}, {prefix}, {lowerprefix} and
// {sha256-checksum} are substituted; without any marker the template is a base
// URL and "/{crate}/{version}/download" is appended.
func DownloadURL(dl, crate, version, cksum string) string {
	const (
		mCrate   = "{crate}"
		mVersion = "{version}"
		mPrefix  = "{prefix}"
		mLower   = "{lowerprefix}"
		mSum     = "{sha256-checksum}"
	)

	if !strings.Contains(dl, mCrate) && !strings.Contains(dl, mVersion) &&
		!strings.Contains(dl, mPrefix) && !strings.Contains(dl, mLower) && !strings.Contains(dl, mSum) {
		return strings.TrimRight(dl, "/") + "/" + crate + "/" + version + "/download"
	}

	prefix := strings.TrimSuffix(IndexPathFor(crate), "/"+crate)

	return strings.NewReplacer(
		mCrate, crate,
		mVersion, version,
		mPrefix, prefix,
		mLower, strings.ToLower(prefix),
		mSum, strings.ToLower(cksum),
	).Replace(dl)
}

// RenderConfig is the config.json a proxy or group repository serves: dl and
// api point at the repository, and a private repository makes cargo send its
// token with every request.
func RenderConfig(baseURL, repoName string, public bool) []byte {
	repoURL := strings.TrimRight(baseURL, "/") + "/repos/" + repoName

	// A struct of strings and a bool always marshals.
	b, _ := json.Marshal(renderedConfig{ //nolint:errchkjson // cannot fail
		DL:           repoURL + "/api/v1/crates",
		API:          repoURL,
		AuthRequired: !public,
	})

	return append(b, '\n')
}

type renderedConfig struct {
	DL           string `json:"dl"`
	API          string `json:"api"`
	AuthRequired bool   `json:"auth-required,omitempty"` //nolint:tagliatelle // cargo registry wire format
}

// stripBuild drops SemVer build metadata: 1.0.0+a and 1.0.0 are one version.
func stripBuild(vers string) string {
	base, _, _ := strings.Cut(vers, "+")
	return base
}

// MergeIndex merges the index files docs of one crate (in member priority
// order): the union of their versions, the first file that lists a version
// providing its entry. Entries keep the order they first appear in.
func MergeIndex(docs [][]byte) ([]byte, error) {
	var (
		out  bytes.Buffer
		seen = map[string]struct{}{}
	)

	for _, doc := range docs {
		entries, err := ParseIndex(doc)
		if err != nil {
			return nil, err
		}

		for _, e := range entries {
			key := stripBuild(e.Vers)
			if _, dup := seen[key]; dup {
				continue
			}

			seen[key] = struct{}{}

			out.Write(e.Line)
			out.WriteByte('\n')
		}
	}

	return out.Bytes(), nil
}
