// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MaxNarInfoBytes bounds a narinfo file. Real ones are a few KiB; the largest
// grow with the number of references.
const MaxNarInfoBytes = 1 << 20

// sha256Prefix starts a hash in the form narinfo uses ("sha256:<nixbase32>").
const sha256Prefix = "sha256:"

// Errors of ParseNarInfo, ParseSHA256 and the narinfo accessors.
var (
	ErrInvalidNarInfo = errors.New("invalid narinfo")
	ErrInvalidHash    = errors.New("invalid sha256 hash")
	ErrInvalidNarFile = errors.New("invalid nar file name")
)

// SHA256 is a SHA-256 digest.
type SHA256 [sha256.Size]byte

// ParseSHA256 reads a SHA-256 hash in any of the spellings Nix tools write:
// "sha256:<nixbase32>", "sha256:<hex>" and the SRI form "sha256-<base64>".
func ParseSHA256(s string) (SHA256, error) {
	var sum SHA256

	switch {
	case strings.HasPrefix(s, sha256Prefix):
		return parseDigest(strings.TrimPrefix(s, sha256Prefix))
	case strings.HasPrefix(s, "sha256-"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "sha256-"))
		if err != nil || len(b) != len(sum) {
			return sum, ErrInvalidHash
		}

		copy(sum[:], b)

		return sum, nil
	}

	return sum, ErrInvalidHash
}

func parseDigest(s string) (SHA256, error) {
	var sum SHA256

	var (
		b   []byte
		err error
	)

	switch len(s) {
	case base32Len(len(sum)):
		b, err = DecodeBase32(s, len(sum))
	case hex.EncodedLen(len(sum)):
		b, err = hex.DecodeString(s)
	default:
		err = ErrInvalidHash
	}

	if err != nil {
		return sum, ErrInvalidHash
	}

	copy(sum[:], b)

	return sum, nil
}

// String returns the narinfo spelling "sha256:<nixbase32>".
func (h SHA256) String() string { return sha256Prefix + EncodeBase32(h[:]) }

// Base32 returns the nixbase32 digits alone, the form used in NAR file names.
func (h SHA256) Base32() string { return EncodeBase32(h[:]) }

// Hex returns the lower case hex digits, the form of a blob id.
func (h SHA256) Hex() string { return hex.EncodeToString(h[:]) }

// NarInfo is the description of one store path in a binary cache.
type NarInfo struct {
	// StorePath is the full path, /nix/store/<hash>-<name>.
	StorePath string
	// URL locates the NAR file relative to the cache, nar/<filehash>.nar.xz.
	URL string
	// Compression is the compression of the file ("none" when it is the bare NAR).
	Compression string
	// FileHash and FileSize describe the file at URL; they are absent from some
	// narinfo of uncompressed NARs.
	FileHash string
	FileSize int64
	// NarHash and NarSize describe the NAR after decompression.
	NarHash string
	NarSize int64
	// References are the base names of the paths this one refers to.
	References []string
	// Deriver is the base name of the .drv that built the path, "" when unknown.
	Deriver string
	// Sigs are the signatures, each "<keyname>:<base64>".
	Sigs []string
	// CA is the content address of a content-addressed path.
	CA string
}

// ParseNarInfo parses and validates the text of a .narinfo file. Unknown
// fields are ignored, a repeated field (other than Sig) is an error.
func ParseNarInfo(data []byte) (*NarInfo, error) {
	if len(data) > MaxNarInfoBytes {
		return nil, fmt.Errorf("%w: too large", ErrInvalidNarInfo)
	}

	n := &NarInfo{Compression: "bzip2"}
	seen := map[string]bool{}

	for len(data) > 0 {
		line, rest, _ := bytes.Cut(data, []byte{'\n'})
		data = rest

		key, value, ok := strings.Cut(string(line), ": ")
		if !ok {
			if strings.TrimSpace(string(line)) == "" {
				continue
			}

			// "References:" with nothing after the colon has no space to cut at.
			key, ok = strings.CutSuffix(string(line), ":")
			if !ok {
				return nil, fmt.Errorf("%w: malformed line", ErrInvalidNarInfo)
			}

			value = ""
		}

		if key != "Sig" && seen[key] {
			return nil, fmt.Errorf("%w: %s given twice", ErrInvalidNarInfo, key)
		}

		seen[key] = true

		if err := n.set(key, value); err != nil {
			return nil, err
		}
	}

	if err := n.validate(); err != nil {
		return nil, err
	}

	return n, nil
}

func (n *NarInfo) set(key, value string) error {
	var err error

	switch key {
	case "StorePath":
		n.StorePath = value
	case "URL":
		n.URL = value
	case "Compression":
		n.Compression = value
	case "FileHash":
		n.FileHash = value
	case "FileSize":
		n.FileSize, err = parseSize(value)
	case "NarHash":
		n.NarHash = value
	case "NarSize":
		n.NarSize, err = parseSize(value)
	case "References":
		n.References = strings.Fields(value)
	case "Deriver":
		if value != "unknown" {
			n.Deriver = value
		}
	case "Sig":
		n.Sigs = append(n.Sigs, value)
	case "CA":
		n.CA = value
	}

	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidNarInfo, key, err)
	}

	return nil
}

func parseSize(s string) (int64, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, errors.New("not a size")
	}

	return v, nil
}

func (n *NarInfo) validate() error {
	if _, _, err := SplitStorePath(n.StorePath); err != nil {
		return fmt.Errorf("%w: StorePath", ErrInvalidNarInfo)
	}

	if n.URL == "" || n.Compression == "" {
		return fmt.Errorf("%w: URL and Compression are required", ErrInvalidNarInfo)
	}

	narHash, err := ParseSHA256(n.NarHash)
	if err != nil {
		return fmt.Errorf("%w: NarHash", ErrInvalidNarInfo)
	}

	n.NarHash = narHash.String()

	if n.FileHash != "" {
		fileHash, err := ParseSHA256(n.FileHash)
		if err != nil {
			return fmt.Errorf("%w: FileHash", ErrInvalidNarInfo)
		}

		n.FileHash = fileHash.String()
	}

	for _, ref := range n.References {
		if _, _, err := SplitBaseName(ref); err != nil {
			return fmt.Errorf("%w: reference %q", ErrInvalidNarInfo, ref)
		}
	}

	if n.Deriver != "" {
		if _, _, err := SplitBaseName(n.Deriver); err != nil {
			return fmt.Errorf("%w: Deriver", ErrInvalidNarInfo)
		}
	}

	for _, sig := range n.Sigs {
		if _, _, ok := strings.Cut(sig, ":"); !ok {
			return fmt.Errorf("%w: Sig", ErrInvalidNarInfo)
		}
	}

	return nil
}

// StoreHash returns the hash part of the store path.
func (n *NarInfo) StoreHash() string {
	hash, _, _ := SplitStorePath(n.StorePath)

	return hash
}

// Name returns the name part of the store path.
func (n *NarInfo) Name() string {
	_, name, _ := SplitStorePath(n.StorePath)

	return name
}

// NarFile validates URL as the location the server accepts for a NAR file,
// nar/<nixbase32 of the file's sha256>.nar[.<ext>], and returns the file name
// below nar/ and the hash it embeds.
func (n *NarInfo) NarFile() (file string, hash SHA256, err error) {
	file, ok := strings.CutPrefix(n.URL, "nar/")
	if !ok {
		return "", hash, ErrInvalidNarFile
	}

	hash, err = ParseNarFile(file)

	return file, hash, err
}

// ParseNarFile checks a file name below nar/ (<nixbase32 sha256>.nar[.<ext>])
// and returns the hash it names.
func ParseNarFile(file string) (SHA256, error) {
	digits, ext, ok := strings.Cut(file, ".nar")
	if !ok || (ext != "" && !validExt(ext)) {
		return SHA256{}, ErrInvalidNarFile
	}

	b, err := DecodeBase32(digits, sha256.Size)
	if err != nil {
		return SHA256{}, ErrInvalidNarFile
	}

	var sum SHA256

	copy(sum[:], b)

	return sum, nil
}

// validExt accepts ".xz", ".zst", ".bz2", ".br", ".lz4", ".gz" and the like:
// a dot and 1 to 8 lower case letters or digits.
func validExt(ext string) bool {
	ext, ok := strings.CutPrefix(ext, ".")
	if !ok || ext == "" || len(ext) > 8 {
		return false
	}

	for i := range len(ext) {
		if (ext[i] < 'a' || ext[i] > 'z') && (ext[i] < '0' || ext[i] > '9') {
			return false
		}
	}

	return true
}

// Fingerprint is the string a signature covers: the store path, the NAR hash
// and size and the references as full paths in name order.
func (n *NarInfo) Fingerprint() (string, error) {
	narHash, err := ParseSHA256(n.NarHash)
	if err != nil {
		return "", err
	}

	refs := slices.Sorted(slices.Values(n.References))
	for i, ref := range refs {
		refs[i] = StoreDir + "/" + ref
	}

	return "1;" + n.StorePath + ";" + narHash.String() + ";" + strconv.FormatInt(n.NarSize, 10) + ";" + strings.Join(refs, ","), nil
}

// String renders the narinfo file. Hashes are written in the nixbase32 form
// Nix expects, whatever spelling the narinfo was parsed from.
func (n *NarInfo) String() string {
	var sb strings.Builder

	sb.WriteString("StorePath: " + n.StorePath + "\n")
	sb.WriteString("URL: " + n.URL + "\n")
	sb.WriteString("Compression: " + n.Compression + "\n")

	if n.FileHash != "" {
		sb.WriteString("FileHash: " + canonicalHash(n.FileHash) + "\n")
	}

	if n.FileHash != "" || n.FileSize != 0 {
		sb.WriteString("FileSize: " + strconv.FormatInt(n.FileSize, 10) + "\n")
	}

	sb.WriteString("NarHash: " + canonicalHash(n.NarHash) + "\n")
	sb.WriteString("NarSize: " + strconv.FormatInt(n.NarSize, 10) + "\n")
	sb.WriteString("References: " + strings.Join(n.References, " ") + "\n")

	if n.Deriver != "" {
		sb.WriteString("Deriver: " + n.Deriver + "\n")
	}

	for _, sig := range n.Sigs {
		sb.WriteString("Sig: " + sig + "\n")
	}

	if n.CA != "" {
		sb.WriteString("CA: " + n.CA + "\n")
	}

	return sb.String()
}

// canonicalHash rewrites a valid hash in the "sha256:<nixbase32>" spelling and
// leaves anything else as it is.
func canonicalHash(s string) string {
	if h, err := ParseSHA256(s); err == nil {
		return h.String()
	}

	return s
}
