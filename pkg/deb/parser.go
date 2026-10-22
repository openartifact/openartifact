// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// Sentinel errors for deb file parsing.
var (
	ErrControlArchiveNotFound   = errors.New("control archive not found in deb file (tried .gz, .xz, .zst)")
	ErrUnsupportedArchiveFormat = errors.New("unsupported control archive format")
	ErrFileNotFoundInTar        = errors.New("file not found in tar archive")
	ErrControlArchiveTooLarge   = errors.New("control archive exceeds max decompressed size")
	ErrDebFileTooLarge          = errors.New("deb file exceeds max size")
	ErrControlFileTooLarge      = errors.New("control file exceeds max size")
)

const (
	maxDebFileSize = 500 * 1024 * 1024 // 500 MiB

	// maxControlArchiveSize caps the decompressed size of a .deb control
	// archive (control.tar.{gz,xz,zst}). Without a cap, io.ReadAll on the
	// decompressor lets a small compressed payload exhaust memory (a
	// decompression bomb).
	maxControlArchiveSize = 64 << 20 // 64 MiB
	maxControlFileSize    = 1 << 20  // 1 MiB
)

// readAllLimited reads at most max+1 bytes from r and errors if the
// result exceeds max, without ever allocating more than max+1 bytes.
func readAllLimited(r io.Reader, limit int64, tooLarge error) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: max %d bytes", tooLarge, limit)
	}

	return data, nil
}

// DebFile represents a parsed .deb file.
type DebFile struct {
	Package      *Package
	ControlFile  *ControlFile
	ControlTarGz []byte
}

// ParseDebFile parses a .deb file from a sequential reader. The input is
// buffered (bounded by maxDebFileSize); callers that already hold the data
// in a file should use ParseDebFileAt to avoid the copy.
func ParseDebFile(r io.Reader) (*DebFile, error) {
	data, err := readAllLimited(r, maxDebFileSize, ErrDebFileTooLarge)
	if err != nil {
		return nil, fmt.Errorf("failed to read deb file: %w", err)
	}

	return ParseDebFileAt(bytes.NewReader(data), int64(len(data)))
}

// ParseDebFileAt parses a .deb file by walking the AR headers with ReadAt and
// reading only the control archive, so memory use is independent of the
// package size.
func ParseDebFileAt(r io.ReaderAt, size int64) (*DebFile, error) {
	if size > maxDebFileSize {
		return nil, fmt.Errorf("failed to read deb file: %w: max %d bytes", ErrDebFileTooLarge, int64(maxDebFileSize))
	}

	magic := make([]byte, len(arMagic))
	if _, err := r.ReadAt(magic, 0); err != nil {
		return nil, fmt.Errorf("failed to parse AR archive: failed to read AR magic: %w", err)
	}

	if string(magic) != arMagic {
		return nil, fmt.Errorf("failed to parse AR archive: %w: %s", ErrInvalidARMagic, string(magic))
	}

	var (
		controlRaw []byte
		controlExt string
	)

	off := int64(len(arMagic))

	for off+arHeaderSize <= size && controlRaw == nil {
		headerBuf := make([]byte, arHeaderSize)
		if _, err := r.ReadAt(headerBuf, off); err != nil {
			return nil, fmt.Errorf("failed to parse AR archive: failed to read AR member header: %w", err)
		}

		header, err := parseARHeader(headerBuf)
		if err != nil {
			return nil, fmt.Errorf("failed to parse AR archive: failed to parse AR header: %w", err)
		}

		if header.Size < 0 {
			return nil, fmt.Errorf("failed to parse AR archive: %w: %d", ErrInvalidSize, header.Size)
		}

		dataOff := off + arHeaderSize
		if header.Size > size-dataOff {
			return nil, fmt.Errorf("failed to parse AR archive: %w: member %q declares size %d beyond end of file",
				ErrARMemberTooLarge, header.Name, header.Size)
		}

		switch header.Name {
		case "control.tar.gz", "control.tar.xz", "control.tar.zst":
			if header.Size > maxControlArchiveSize {
				return nil, fmt.Errorf("failed to parse AR archive: %w: member %q declares size %d, max %d",
					ErrARMemberTooLarge, header.Name, header.Size, int64(maxControlArchiveSize))
			}

			controlRaw = make([]byte, header.Size)
			if _, err := r.ReadAt(controlRaw, dataOff); err != nil {
				return nil, fmt.Errorf("failed to parse AR archive: failed to read AR member data: %w", err)
			}

			controlExt = header.Name
		}

		off = dataOff + header.Size
		if header.Size%arMemberAlign != 0 {
			off += arMemberAlign - (header.Size % arMemberAlign)
		}
	}

	if controlRaw == nil {
		return nil, ErrControlArchiveNotFound
	}

	debFile := &DebFile{ControlTarGz: controlRaw}

	controlFile, err := extractControlFile(controlRaw, controlExt)
	if err != nil {
		return nil, fmt.Errorf("failed to extract control file: %w", err)
	}

	debFile.ControlFile = controlFile

	pkg, err := controlFile.ParsePackage()
	if err != nil {
		return nil, fmt.Errorf("failed to parse package metadata: %w", err)
	}

	debFile.Package = pkg

	return debFile, nil
}

// extractControlFile extracts the control file from a compressed tar archive.
// ext is the full member name (e.g. "control.tar.xz") and is used to pick the decompressor.
func extractControlFile(raw []byte, ext string) (*ControlFile, error) {
	var tarData []byte

	switch {
	case strings.HasSuffix(ext, ".gz"):
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("failed to decompress %s: %w", ext, err)
		}
		defer func() { _ = gr.Close() }()

		tarData, err = readAllLimited(gr, maxControlArchiveSize, ErrControlArchiveTooLarge)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", ext, err)
		}

	case strings.HasSuffix(ext, ".xz"):
		if err := checkXZDictCap(raw); err != nil {
			return nil, fmt.Errorf("failed to decompress %s: %w", ext, err)
		}

		xr, err := xz.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("failed to decompress %s: %w", ext, err)
		}

		tarData, err = readAllLimited(xr, maxControlArchiveSize, ErrControlArchiveTooLarge)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", ext, err)
		}

	case strings.HasSuffix(ext, ".zst"):
		zr, err := zstd.NewReader(bytes.NewReader(raw),
			zstd.WithDecoderMaxWindow(maxControlDictSize), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, fmt.Errorf("failed to decompress %s: %w", ext, err)
		}
		defer zr.Close()

		tarData, err = readAllLimited(zr, maxControlArchiveSize, ErrControlArchiveTooLarge)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", ext, err)
		}

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedArchiveFormat, ext)
	}

	controlData, err := extractFileFromTar(tarData, "control")
	if err != nil {
		return nil, fmt.Errorf("failed to extract control file from tar: %w", err)
	}

	controlFile, err := ParseControlFile(bytes.NewReader(controlData))
	if err != nil {
		return nil, fmt.Errorf("failed to parse control file: %w", err)
	}

	return controlFile, nil
}

// extractFileFromTar extracts a file from a tar archive using the standard library.
func extractFileFromTar(tarData []byte, filename string) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(tarData))

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read tar entry: %w", err)
		}

		// Match by exact name or path suffix (e.g., "./control" matches "control")
		name := strings.TrimPrefix(header.Name, "./")
		if name == filename || strings.HasSuffix(name, "/"+filename) {
			data, err := readAllLimited(tr, maxControlFileSize, ErrControlFileTooLarge)
			if err != nil {
				return nil, fmt.Errorf("failed to read file data: %w", err)
			}

			return data, nil
		}
	}

	return nil, fmt.Errorf("%w: '%s'", ErrFileNotFoundInTar, filename)
}

// GetMetadata returns package metadata as a map.
func (df *DebFile) GetMetadata() map[string]any {
	if df.Package == nil {
		return make(map[string]any)
	}

	pkg := df.Package
	metadata := map[string]any{
		"package":      pkg.Package,
		"version":      pkg.Version,
		"architecture": pkg.Architecture,
		"maintainer":   pkg.Maintainer,
		"description":  pkg.Description,
		"section":      pkg.Section,
		"priority":     pkg.Priority,
		"homepage":     pkg.Homepage,
		"essential":    pkg.Essential,
		"multi_arch":   pkg.MultiArch,
	}

	// Add dependency information
	if pkg.Depends != "" {
		metadata["depends"] = pkg.Depends
	}

	if pkg.PreDepends != "" {
		metadata["pre_depends"] = pkg.PreDepends
	}

	if pkg.Recommends != "" {
		metadata["recommends"] = pkg.Recommends
	}

	if pkg.Suggests != "" {
		metadata["suggests"] = pkg.Suggests
	}

	if pkg.Conflicts != "" {
		metadata["conflicts"] = pkg.Conflicts
	}

	if pkg.Breaks != "" {
		metadata["breaks"] = pkg.Breaks
	}

	if pkg.Replaces != "" {
		metadata["replaces"] = pkg.Replaces
	}

	if pkg.Provides != "" {
		metadata["provides"] = pkg.Provides
	}

	// Add checksums
	if pkg.MD5sum != "" {
		metadata["md5sum"] = pkg.MD5sum
	}

	if pkg.SHA1 != "" {
		metadata["sha1"] = pkg.SHA1
	}

	if pkg.SHA256 != "" {
		metadata["sha256"] = pkg.SHA256
	}

	// Add custom fields
	for k, v := range pkg.Custom {
		metadata["custom_"+k] = v
	}

	return metadata
}
