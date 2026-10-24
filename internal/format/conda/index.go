// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"encoding/json"
	"io"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	// maxIndexJSONBytes bounds how much of info/index.json is read.
	maxIndexJSONBytes = 1 << 20
	// maxDecompressedBytes bounds the decompressed bytes read from a package
	// while looking for info/index.json, so a bzip2 or zstd bomb inside the
	// upload cannot burn unbounded CPU or memory.
	maxDecompressedBytes = 256 << 20
	// maxTarEntries bounds the tar entries visited for the same reason.
	maxTarEntries = 100_000
	// maxZstdWindow bounds the zstd decoder window of a .conda info member.
	maxZstdWindow = 64 << 20
)

// repodataExtraKeys returns the info/index.json fields copied verbatim into a
// repodata.json entry besides the ones with their own column.
func repodataExtraKeys() []string {
	return []string{
		"arch", "constrains", "features", "license", "license_family", "noarch", "platform", "track_features",
	}
}

// packageIndex is what the server keeps of a package's info/index.json.
type packageIndex struct {
	BuildNumber int
	// Timestamp is milliseconds since the epoch, 0 when absent.
	Timestamp int64
	Depends   []string
	// License is the declared license ("license"; "license_family" is not a
	// license), "" when undeclared.
	License string
	// Extra holds the repodataExtraKeys() present in the index as a JSON object.
	Extra json.RawMessage
}

// readPackageIndex returns the parsed info/index.json of a package file, or
// the zero packageIndex when the archive cannot be read or has none: the upload
// is still accepted, with empty dependencies and no license.
func readPackageIndex(ext string, r io.ReaderAt, size int64) packageIndex {
	var raw []byte

	switch ext {
	case models.CondaExtTarBz2:
		raw = indexFromTar(bzip2.NewReader(io.NewSectionReader(r, 0, size)), defaultTarLimits())
	case models.CondaExtConda:
		raw = indexFromConda(r, size)
	}

	return parsePackageIndex(raw)
}

func parsePackageIndex(raw []byte) packageIndex {
	idx := packageIndex{Depends: []string{}, Extra: json.RawMessage(`{}`)}

	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return idx
	}

	var depends []string
	if json.Unmarshal(fields["depends"], &depends) == nil && depends != nil && !slices.ContainsFunc(depends, hasNUL) {
		idx.Depends = depends
	}

	var buildNumber int
	if json.Unmarshal(fields["build_number"], &buildNumber) == nil && buildNumber > 0 && buildNumber <= math.MaxInt32 {
		idx.BuildNumber = buildNumber
	}

	var timestamp float64
	if json.Unmarshal(fields["timestamp"], &timestamp) == nil && timestamp > 0 && timestamp < math.MaxInt64 {
		idx.Timestamp = int64(timestamp)
	}

	var license string
	if json.Unmarshal(fields["license"], &license) == nil {
		idx.License = strings.TrimSpace(license)
	}

	extra := make(map[string]json.RawMessage, len(repodataExtraKeys()))

	for _, k := range repodataExtraKeys() {
		if v, ok := fields[k]; ok && string(v) != "null" {
			if v, ok = storableJSON(v); ok {
				extra[k] = v
			}
		}
	}

	if b, err := json.Marshal(extra); err == nil {
		idx.Extra = b
	}

	return idx
}

// hasNUL reports whether s holds a NUL, which PostgreSQL text cannot.
func hasNUL(s string) bool { return strings.ContainsRune(s, 0) }

// storableJSON returns v re-encoded the way PostgreSQL jsonb accepts it, and
// false for a value jsonb cannot hold (a NUL in a string or key). Re-encoding
// replaces invalid UTF-8 and unpaired surrogate escapes, which jsonb refuses, by
// U+FFFD, as the decoder reads them.
func storableJSON(v json.RawMessage) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()

	var decoded any
	if dec.Decode(&decoded) != nil || jsonHasNUL(decoded) {
		return nil, false
	}

	out, err := json.Marshal(decoded)

	return out, err == nil
}

func jsonHasNUL(v any) bool {
	switch v := v.(type) {
	case string:
		return hasNUL(v)
	case []any:
		return slices.ContainsFunc(v, jsonHasNUL)
	case map[string]any:
		for k, e := range v {
			if hasNUL(k) || jsonHasNUL(e) {
				return true
			}
		}
	}

	return false
}

// indexFromConda reads info/index.json from the info-*.tar.zst member of a
// .conda (zip) archive.
func indexFromConda(r io.ReaderAt, size int64) []byte {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil
	}

	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, "info-") || !strings.HasSuffix(f.Name, ".tar.zst") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil
		}

		defer func() { _ = rc.Close() }()

		dec, err := zstd.NewReader(rc,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxWindow(maxZstdWindow),
			zstd.WithDecoderMaxMemory(maxDecompressedBytes))
		if err != nil {
			return nil
		}

		defer dec.Close()

		return indexFromTar(dec, defaultTarLimits())
	}

	return nil
}

// tarLimits bounds the work of looking for info/index.json in a tar stream.
type tarLimits struct {
	// bytes is the most decompressed bytes read.
	bytes int64
	// entries is the most tar entries visited.
	entries int
}

func defaultTarLimits() tarLimits {
	return tarLimits{bytes: maxDecompressedBytes, entries: maxTarEntries}
}

// indexFromTar returns the bytes of info/index.json from a decompressed tar
// stream, or nil when it is not found within the limits.
func indexFromTar(r io.Reader, lim tarLimits) []byte {
	tr := tar.NewReader(io.LimitReader(r, lim.bytes))

	for range lim.entries {
		hdr, err := tr.Next()
		if err != nil {
			return nil
		}

		if path.Clean(hdr.Name) != "info/index.json" {
			continue
		}

		b, err := io.ReadAll(io.LimitReader(tr, maxIndexJSONBytes))
		if err != nil {
			return nil
		}

		return b
	}

	return nil
}
