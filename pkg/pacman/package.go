// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Package compressions, the extension of the package file after ".pkg.tar.".
const (
	CompressionZstd = "zst"
	CompressionXZ   = "xz"
	CompressionGzip = "gz"
)

// Limits on what reading one package may cost. The package is untrusted input
// and a few KiB of zstd can decompress to gigabytes.
const (
	// MaxUncompressedBytes bounds the decompressed tar stream.
	MaxUncompressedBytes int64 = 32 << 30
	// MaxFileEntries bounds the entries of the archive.
	MaxFileEntries = 2_000_000
	// MaxFileListBytes bounds the bytes of the file list kept for the files database.
	MaxFileListBytes = 64 << 20
	// MaxEntryNameLen bounds one archive path.
	MaxEntryNameLen = 4096
	// zstdMaxWindow bounds the decoder's window: makepkg uses --ultra only up to 22
	// (4 MiB windows by default, 128 MiB at most).
	zstdMaxWindow = 128 << 20
)

// Errors reading a package.
var (
	ErrNotPackage      = errors.New("pacman: not a pacman package")
	ErrNoPkgInfo       = errors.New("pacman: package has no .PKGINFO")
	ErrPackageTooLarge = errors.New("pacman: package expands beyond the limit")
	ErrUnsafeEntry     = errors.New("pacman: unsafe path in package")
)

// Package is what the repository database needs to know about a package file.
type Package struct {
	Info        *PkgInfo
	Compression string
	// Files is the file list as the files database writes it: paths relative to
	// the root, directories with a trailing slash, dot files (.PKGINFO,
	// .BUILDINFO, .MTREE, .INSTALL) left out.
	Files []string
}

// ReadPackage reads a .pkg.tar.{zst,xz,gz} from r: its .PKGINFO and its file
// list. Compression is detected from the content, not the name.
func ReadPackage(r io.Reader) (*Package, error) {
	br := bufio.NewReaderSize(r, 64<<10)

	head, err := br.Peek(6)
	if err != nil && len(head) < 4 {
		return nil, ErrNotPackage
	}

	var (
		dec         io.Reader
		compression string
		closeDec    func()
	)

	switch {
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		zr, zerr := zstd.NewReader(
			br,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxWindow(zstdMaxWindow),
			zstd.WithDecoderLowmem(true),
		)
		if zerr != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotPackage, zerr)
		}

		dec, compression, closeDec = zr, CompressionZstd, zr.Close
	case bytes.HasPrefix(head, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		xr, xerr := xz.NewReader(br)
		if xerr != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotPackage, xerr)
		}

		dec, compression = xr, CompressionXZ
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		gr, gerr := gzip.NewReader(br)
		if gerr != nil {
			return nil, fmt.Errorf("%w: %w", ErrNotPackage, gerr)
		}

		dec, compression, closeDec = gr, CompressionGzip, func() { _ = gr.Close() }
	default:
		return nil, ErrNotPackage
	}

	if closeDec != nil {
		defer closeDec()
	}

	pkg, err := readTar(&limitedStream{r: dec, left: MaxUncompressedBytes})
	if err != nil {
		return nil, err
	}

	pkg.Compression = compression

	return pkg, nil
}

// limitedStream fails with ErrPackageTooLarge once more than left bytes are read.
type limitedStream struct {
	r    io.Reader
	left int64
}

func (l *limitedStream) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, ErrPackageTooLarge
	}

	if int64(len(p)) > l.left {
		p = p[:l.left]
	}

	n, err := l.r.Read(p)
	l.left -= int64(n)

	return n, err //nolint:wrapcheck // plain passthrough
}

func readTar(r io.Reader) (*Package, error) {
	tr := tar.NewReader(r)
	pkg := &Package{}

	var (
		entries   int
		listBytes int
		sawHeader bool
	)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			if errors.Is(err, ErrPackageTooLarge) {
				return nil, err
			}

			if !sawHeader {
				return nil, fmt.Errorf("%w: %w", ErrNotPackage, err)
			}

			return nil, fmt.Errorf("pacman: reading package archive: %w", err)
		}

		sawHeader = true

		entries++
		if entries > MaxFileEntries {
			return nil, fmt.Errorf("%w: too many entries", ErrPackageTooLarge)
		}

		name, err := cleanEntryName(hdr.Name)
		if err != nil {
			return nil, err
		}

		if name == ".PKGINFO" && hdr.Typeflag != tar.TypeDir {
			if pkg.Info != nil {
				return nil, fmt.Errorf("%w: duplicate .PKGINFO", ErrInvalidField)
			}

			pkg.Info, err = ParsePkgInfo(tr)
			if err != nil {
				return nil, err
			}

			continue
		}

		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}

		if hdr.Typeflag == tar.TypeDir {
			name += "/"
		}

		listBytes += len(name) + 1
		if listBytes > MaxFileListBytes {
			return nil, fmt.Errorf("%w: file list too large", ErrPackageTooLarge)
		}

		pkg.Files = append(pkg.Files, name)
	}

	if pkg.Info == nil {
		return nil, ErrNoPkgInfo
	}

	return pkg, nil
}

// cleanEntryName normalizes an archive path ("./usr/bin/x" to "usr/bin/x") and
// refuses what could not be a package path: absolute or parent-relative paths,
// control characters (a newline would split a files database line) and over-long
// names. The empty string is the archive root.
func cleanEntryName(raw string) (string, error) {
	if len(raw) > MaxEntryNameLen {
		return "", fmt.Errorf("%w: path too long", ErrUnsafeEntry)
	}

	for i := range len(raw) {
		if raw[i] < ' ' || raw[i] == 0x7f {
			return "", fmt.Errorf("%w: control character in path", ErrUnsafeEntry)
		}
	}

	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%w: absolute path", ErrUnsafeEntry)
	}

	for seg := range strings.SplitSeq(raw, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: parent directory in path", ErrUnsafeEntry)
		}
	}

	name := path.Clean(raw)
	if name == "." {
		return "", nil
	}

	return name, nil
}

// PackageFileName returns the file name makepkg gives a package:
// name-version-arch.pkg.tar.ext, the colon of an epoch included.
func PackageFileName(name, version, arch, ext string) string {
	return name + "-" + version + "-" + arch + ".pkg.tar." + ext
}

// SplitPackageFileName returns the extension ("zst", "xz", "gz") of a package
// file name and the part before ".pkg.tar.". ok is false for any other name.
func SplitPackageFileName(filename string) (string, string, bool) {
	i := strings.Index(filename, ".pkg.tar.")
	if i <= 0 {
		return "", "", false
	}

	ext := filename[i+len(".pkg.tar."):]
	switch ext {
	case CompressionZstd, CompressionXZ, CompressionGzip:
		return filename[:i], ext, true
	}

	return "", "", false
}

// FileNameMatches reports whether filename is the name the package described by
// info and ext must be published under: the one makepkg writes, or the same with
// the colon of an epoch written as a hyphen (the spelling of tools that avoid
// colons in file names).
func FileNameMatches(filename string, info *PkgInfo, ext string) bool {
	return filename == PackageFileName(info.Name, info.Version, info.Arch, ext) ||
		filename == PackageFileName(info.Name, strings.ReplaceAll(info.Version, ":", "-"), info.Arch, ext)
}
