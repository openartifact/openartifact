// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package recovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openartifact/openartifact/migrations"
)

const (
	// SidecarMagic is the value of the "openartifact_sidecar" field: the
	// sidecar format version. A reader rejects any other value.
	SidecarMagic = 1

	// MaxSidecarBytes bounds what the rebuild reads for one sidecar (an npm
	// manifest of a package with hundreds of versions is the largest row).
	MaxSidecarBytes = 64 << 20

	// MetaKind and MetaTable are the blob metadata keys that mark a sidecar.
	// They are lowercase letters only: S3 lower-cases user metadata names and
	// Azure restricts them to identifiers and returns them canonicalised, so
	// readers look them up case-insensitively (metaValue).
	MetaKind  = "oakind"
	MetaTable = "oatable"
	// MetaKindSidecar is the MetaKind value of sidecar blobs.
	MetaKindSidecar = "sidecar"

	// sidecarPrefix is how every sidecar starts: the magic field is first.
	sidecarPrefix = `{"openartifact_sidecar":`
)

// Sidecar decoding errors; callers match them with errors.Is.
var (
	ErrSidecarDigest   = errors.New("sidecar content does not hash to its blob id")
	ErrSidecarTooLarge = errors.New("sidecar exceeds the size limit")
	ErrSidecarFormat   = errors.New("not a sidecar")
	ErrSidecarVersion  = errors.New("unsupported sidecar format version")
	ErrSidecarTable    = errors.New("sidecar names a table that is not journaled")
	ErrSidecarKey      = errors.New("sidecar key does not match its row")
	ErrSidecarRow      = errors.New("sidecar row is not a JSON object")
)

// Sidecar is the JSON document stored for one journaled row.
type Sidecar struct {
	Magic     int             `json:"openartifact_sidecar"`
	Schema    int             `json:"schema"`
	Table     string          `json:"table"`
	Key       string          `json:"key"`
	WrittenAt time.Time       `json:"written_at"`
	Row       json.RawMessage `json:"row"`
}

// SchemaVersion is the number of the newest embedded migration: the schema
// version stamped into every sidecar.
func SchemaVersion() int {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return 0
	}

	latest := 0

	for _, e := range entries {
		n := 0

		for _, c := range e.Name() {
			if c < '0' || c > '9' {
				break
			}

			n = n*10 + int(c-'0')
		}

		latest = max(latest, n)
	}

	return latest
}

// EncodeSidecar serializes a sidecar for table t. row is the compact JSON image
// of the row (jsonb text minus the excluded columns). It returns the bytes and
// their sha256 (the blob id).
func EncodeSidecar(t *Table, key string, schema int, writtenAt time.Time, row []byte) ([]byte, string, error) {
	sc := Sidecar{Magic: SidecarMagic, Schema: schema, Table: t.Name, Key: key, WrittenAt: writtenAt.UTC(), Row: row}

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(&sc); err != nil {
		return nil, "", fmt.Errorf("encode sidecar: %w", err)
	}

	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(data) > MaxSidecarBytes {
		return nil, "", fmt.Errorf("%w: %s %s is %d bytes", ErrSidecarTooLarge, t.Name, key, len(data))
	}

	sum := sha256.Sum256(data)

	return data, hex.EncodeToString(sum[:]), nil
}

// DecodeSidecar parses and validates sidecar bytes read from blob id: size,
// content digest, magic, a journaled table, a key of the table's shape that
// equals the key columns of the row image. Unknown fields are ignored so later
// versions can add some.
func DecodeSidecar(id string, data []byte) (*Sidecar, *Table, error) {
	if len(data) > MaxSidecarBytes {
		return nil, nil, ErrSidecarTooLarge
	}

	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != id {
		return nil, nil, ErrSidecarDigest
	}

	if !LooksLikeSidecar(data) {
		return nil, nil, ErrSidecarFormat
	}

	var sc Sidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrSidecarFormat, err)
	}

	if sc.Magic != SidecarMagic {
		return nil, nil, fmt.Errorf("%w: %d", ErrSidecarVersion, sc.Magic)
	}

	t, ok := Lookup(sc.Table)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrSidecarTable, sc.Table)
	}

	var row map[string]json.RawMessage
	if err := json.Unmarshal(sc.Row, &row); err != nil || row == nil {
		return nil, nil, ErrSidecarRow
	}

	if err := checkKey(t, sc.Key, row); err != nil {
		return nil, nil, err
	}

	return &sc, t, nil
}

// checkKey verifies that key has the shape of t's primary key and equals the
// values of the key columns inside row.
func checkKey(t *Table, key string, row map[string]json.RawMessage) error {
	want, ok := t.SplitKey(key)
	if !ok {
		return fmt.Errorf("%w: malformed key %q", ErrSidecarKey, key)
	}

	for i, k := range t.Key {
		var got string
		if err := json.Unmarshal(row[k.Name], &got); err != nil || got != want[i] {
			return fmt.Errorf("%w: column %s", ErrSidecarKey, k.Name)
		}
	}

	return nil
}

// LooksLikeSidecar reports whether b (at least the first bytes of a blob) starts
// like a sidecar document. The rebuild's --deep scan uses it on blobs whose
// backend metadata lost the marker.
func LooksLikeSidecar(b []byte) bool {
	return bytes.HasPrefix(b, []byte(sidecarPrefix))
}

// SidecarPrefixLen is how many leading bytes LooksLikeSidecar needs.
func SidecarPrefixLen() int { return len(sidecarPrefix) }

// SidecarMetadata returns the blob metadata Put must carry for a sidecar of
// table whose content hashes to sha.
func SidecarMetadata(table, sha string) map[string]string {
	return map[string]string{"sha256": sha, MetaKind: MetaKindSidecar, MetaTable: table}
}

// IsSidecarMeta reports whether blob metadata carries the sidecar marker.
func IsSidecarMeta(meta map[string]string) bool {
	return metaValue(meta, MetaKind) == MetaKindSidecar
}

// metaValue looks a metadata key up case-insensitively (S3 lower-cases names,
// Azure canonicalises them).
func metaValue(meta map[string]string, key string) string {
	for k, v := range meta {
		if strings.EqualFold(k, key) {
			return v
		}
	}

	return ""
}
