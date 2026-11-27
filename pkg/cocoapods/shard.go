// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cocoapods

import (
	"bytes"
	"crypto/md5" //nolint:gosec // CocoaPods shards pods by the MD5 of their name; it is a bucket, not a security property
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrDocument is an upstream document that cannot be used.
var ErrDocument = errors.New("unusable cocoapods document")

// MaxVersionFile bounds CocoaPods-version.yml.
const MaxVersionFile = 64 << 10

// DefaultPrefixLengths is the sharding of the trunk CDN and of every repository
// whose CocoaPods-version.yml names none.
var DefaultPrefixLengths = []int{1, 1, 1} //nolint:gochecknoglobals // immutable default

// VersionDoc is CocoaPods-version.yml.
type VersionDoc struct {
	Min           string `yaml:"min"`
	Last          string `yaml:"last"`
	PrefixLengths []int  `yaml:"prefix_lengths"` //nolint:tagliatelle // the CDN's key
}

// ParseVersionDoc reads CocoaPods-version.yml. The prefix lengths must be a
// sharding the MD5 of a name can serve; a document without any gets
// DefaultPrefixLengths.
func ParseVersionDoc(body []byte) (VersionDoc, error) {
	if len(body) > MaxVersionFile {
		return VersionDoc{}, fmt.Errorf("%w: CocoaPods-version.yml is larger than %d bytes", ErrDocument, MaxVersionFile)
	}

	var d VersionDoc
	if err := yaml.Unmarshal(body, &d); err != nil {
		return VersionDoc{}, fmt.Errorf("%w: CocoaPods-version.yml: %w", ErrDocument, err)
	}

	if d.Min == "" && d.Last == "" && d.PrefixLengths == nil {
		return VersionDoc{}, fmt.Errorf("%w: CocoaPods-version.yml names no version and no prefix lengths", ErrDocument)
	}

	if d.PrefixLengths == nil {
		d.PrefixLengths = DefaultPrefixLengths
	}

	total := 0

	for _, n := range d.PrefixLengths {
		if n < 1 || n > md5.Size*2 {
			return VersionDoc{}, fmt.Errorf("%w: prefix length %d", ErrDocument, n)
		}

		total += n
	}

	if len(d.PrefixLengths) > maxDirs || total > md5.Size*2 {
		return VersionDoc{}, fmt.Errorf("%w: prefix lengths %v do not fit an MD5", ErrDocument, d.PrefixLengths)
	}

	return d, nil
}

// ShardParts is the hex prefixes of the MD5 of name, one per prefix length: the
// directories of the pod's podspecs and the parts of its shard's file name.
func ShardParts(name string, prefixLengths []int) []string {
	sum := md5.Sum([]byte(name)) //nolint:gosec // see the import
	hexSum := hex.EncodeToString(sum[:])
	parts := make([]string, 0, len(prefixLengths))
	pos := 0

	for _, n := range prefixLengths {
		if n < 1 || pos+n > len(hexSum) {
			break
		}

		parts = append(parts, hexSum[pos:pos+n])
		pos += n
	}

	return parts
}

// ShardDir is the directory of the podspecs of name, "X/Y/Z" for the default sharding.
func ShardDir(name string, prefixLengths []int) string {
	return strings.Join(ShardParts(name, prefixLengths), "/")
}

// ShardFile is the file name of the shard that lists name.
func ShardFile(name string, prefixLengths []int) string {
	parts := ShardParts(name, prefixLengths)
	if len(parts) == 0 {
		return shardBase[:len(shardBase)-1] + shardExt
	}

	return shardBase + strings.Join(parts, "_") + shardExt
}

// PodVersions is one line of a shard: a pod and every version of it.
type PodVersions struct {
	Name     string
	Versions []string
}

// lines calls fn for every non-blank line of a shard; fn returns false to stop.
// A malformed line is an error: the line number and the reason.
func lines(body []byte, fn func(name []byte, versions [][]byte) bool) error {
	n := 0

	for len(body) > 0 {
		var line []byte

		line, body, _ = bytes.Cut(body, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		n++

		if len(line) == 0 {
			continue
		}

		fields := bytes.Split(line, []byte{'/'})
		if len(fields) < 2 {
			return fmt.Errorf("%w: shard line %d lists no version", ErrDocument, n)
		}

		for _, f := range fields {
			if err := checkField(f); err != nil {
				return fmt.Errorf("%w: shard line %d: %w", ErrDocument, n, err)
			}
		}

		if !fn(fields[0], fields[1:]) {
			return nil
		}
	}

	return nil
}

var errBadField = errors.New("empty name or version, or a character a name cannot have")

func checkField(f []byte) error {
	if len(f) == 0 || len(f) > MaxSegmentLen {
		return errBadField
	}

	for _, r := range string(f) {
		if badRune(r) {
			return errBadField
		}
	}

	return nil
}

// VerifyShard reports whether body is a shard document: every line is
// "Name/Version/Version/..." with no empty or unprintable field.
func VerifyShard(body []byte) error {
	return lines(body, func([]byte, [][]byte) bool { return true })
}

// VersionsOf is the versions the shard lists for the pod name; false when it
// lists none.
func VersionsOf(body []byte, name string) ([]string, bool, error) {
	var (
		out   []string
		found bool
	)

	err := lines(body, func(n []byte, versions [][]byte) bool {
		if string(n) != name {
			return true
		}

		found = true

		for _, v := range versions {
			out = append(out, string(v))
		}

		return false
	})
	if err != nil {
		return nil, false, err
	}

	return out, found, nil
}

// FilterShard returns the shard without the versions keep refuses and without
// the pods none of whose versions is left. A nil keep returns body itself.
func FilterShard(body []byte, keep func(name, version string) bool) ([]byte, error) {
	if keep == nil {
		return body, VerifyShard(body)
	}

	out := make([]byte, 0, len(body))

	err := lines(body, func(name []byte, versions [][]byte) bool {
		pod := string(name)
		kept := 0
		start := len(out)

		out = append(out, name...)

		for _, v := range versions {
			if keep(pod, string(v)) {
				out = append(out, '/')
				out = append(out, v...)
				kept++
			}
		}

		if kept == 0 {
			out = out[:start]
			return true
		}

		out = append(out, '\n')

		return true
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// ShardFileParts is the hex prefixes a shard file name carries: the prefix lengths
// of its repository are their lengths. False when file is not a shard name.
func ShardFileParts(file string) ([]string, bool) {
	if !ValidShardName(file) {
		return nil, false
	}

	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(file, shardBase), shardExt), "_")

	return parts, len(parts) <= maxDirs
}

// VerifyShardFile reports whether body is the shard document file: a shard whose
// every pod hashes into it, so a page served under the wrong name, or a shard of
// another sharding, is not taken for it.
func VerifyShardFile(body []byte, file string) error {
	parts, ok := ShardFileParts(file)
	if !ok {
		return fmt.Errorf("%w: %q is not a shard name", ErrDocument, file)
	}

	lengths := make([]int, len(parts))
	for i, p := range parts {
		lengths[i] = len(p)
	}

	var misplaced string

	err := lines(body, func(name []byte, _ [][]byte) bool {
		if !slices.Equal(ShardParts(string(name), lengths), parts) {
			misplaced = string(name)
			return false
		}

		return true
	})
	if err != nil {
		return err
	}

	if misplaced != "" {
		return fmt.Errorf("%w: pod %q does not belong to %s", ErrDocument, misplaced, file)
	}

	return nil
}

// VerifyDeprecated reports whether body is a deprecated_podspecs.txt: one
// "Specs/..." path per line.
func VerifyDeprecated(body []byte) error {
	n := 0

	for len(body) > 0 {
		var line []byte

		line, body, _ = bytes.Cut(body, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		n++

		if len(line) == 0 {
			continue
		}

		if !bytes.HasPrefix(line, []byte(specsDir)) || checkField(bytes.ReplaceAll(line, []byte{'/'}, []byte{'_'})) != nil {
			return fmt.Errorf("%w: deprecated_podspecs.txt line %d is not a spec path", ErrDocument, n)
		}
	}

	return nil
}

// VerifyAllPods reports whether body is an all_pods.txt: one pod name per line.
func VerifyAllPods(body []byte) error {
	n := 0

	for len(body) > 0 {
		var line []byte

		line, body, _ = bytes.Cut(body, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		n++

		if len(line) == 0 {
			continue
		}

		if !ValidName(string(line)) {
			return fmt.Errorf("%w: all_pods.txt line %d is not a pod name", ErrDocument, n)
		}
	}

	return nil
}
