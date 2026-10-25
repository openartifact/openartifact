// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package licensemeta reads the licenses a package declares from the package
// file itself: the metadata inside the artifact (a wheel's METADATA, an npm
// tarball's package.json, a crate's Cargo.toml, a nupkg's .nuspec, a .pom).
//
// Upload requests carry their own claims about a package (twine's form fields,
// npm's publish JSON, cargo's publish metadata); a client that cannot or will
// not send them (curl, minimal tools, imports) or that lies in them would
// otherwise decide what license policies see. The artifact is the source of
// truth, request fields are only a fallback for what the artifact does not say.
//
// Artifact files are untrusted. Reads go through internal/ai/manifest in
// strict mode: every member is size-capped, decompression is bounded, member
// names are normalised (absolute and parent-escaping names never match, nothing
// is extracted to disk), and an archive with two candidates for the same
// metadata file is refused because tools differ on which of them they read.
package licensemeta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

// memberLimit bounds one metadata file read from an artifact.
const memberLimit = 1 << 20

var (
	// ErrUnsupported means licenses cannot be read from this kind of file.
	ErrUnsupported = errors.New("licenses cannot be read from this artifact")

	// ErrNoMetadata means the artifact was read but holds no metadata file.
	ErrNoMetadata = errors.New("artifact holds no package metadata")

	// ErrUnsafe means the artifact is too large to read safely (a metadata
	// file or decompressed stream over the limits) or holds more than one
	// candidate for its metadata file. Callers refuse such an artifact: it is
	// either a decompression bomb or built to be read differently by
	// different tools.
	ErrUnsafe = errors.New("artifact metadata cannot be read safely")
)

// Result is what an artifact declares.
type Result struct {
	// Licenses are the declared licenses, as written (no normalisation): an
	// SPDX expression, an alias, a classifier or free text.
	Licenses []string
	// Source names where they were read from: "License-Expression",
	// "License", "classifier", "package.json", "Cargo.toml", ".nuspec" or
	// "pom".
	Source string
	// Parent is the <parent> a Maven pom declares; set only for a pom that
	// declares no license of its own, whose licenses are then the parent's
	// (see InheritPOMLicenses). Nil otherwise.
	Parent *pkgmaven.Parent
}

// Empty reports whether the artifact declares no license.
func (r Result) Empty() bool { return len(r.Licenses) == 0 }

// Supported reports whether licenses can be read from a file of this format.
func Supported(format, filename string) bool {
	switch format {
	case "pypi", "npm", "cargo", "nuget":
		return manifest.Supported(format, filename)
	case "maven", "sbt":
		return manifest.Supported("maven", filename) && !strings.HasSuffix(strings.ToLower(filename), ".xml")
	}

	return false
}

// Expected reports whether a file of this format is a package that declares
// its own license metadata: a wheel or sdist, an npm tarball, a crate, a nupkg,
// a gem or a .pom. For these a failure to read the licenses (an unsupported
// file type such as a PyPI .egg, a damaged archive) leaves nothing but the
// client's claims to judge the package by. Maven jars, checksums and
// maven-metadata.xml are not: the .pom beside them is the authority.
func Expected(format, filename string) bool {
	switch format {
	case "pypi", "npm", "cargo", "nuget", "rubygems":
		return true
	case "maven", "sbt":
		return strings.HasSuffix(strings.ToLower(filename), ".pom")
	}

	return false
}

// Read reads the licenses of an artifact; open returns a fresh reader over its
// bytes and is called once per attempt. The error is ErrUnsupported,
// ErrNoMetadata, ErrUnsafe, or an I/O or parse failure (the file is not the
// archive its name says, the metadata is not well formed).
func Read(ctx context.Context, format, filename string, open func() (io.ReadCloser, error)) (Result, error) {
	if !Supported(format, filename) {
		return Result{}, ErrUnsupported
	}

	limit := int64(memberLimit)
	if format == "maven" || format == "sbt" {
		limit = pkgmaven.MaxPOMBytes
	}

	parts, err := manifest.ExtractParts(ctx, manifest.Source{
		Format:      jvmFormat(format),
		Filename:    filename,
		Open:        open,
		MemberLimit: limit,
		Strict:      true,
	})

	switch {
	case err == nil:
	case errors.Is(err, manifest.ErrNotFound):
		return Result{}, ErrNoMetadata
	case errors.Is(err, manifest.ErrTooLarge), errors.Is(err, manifest.ErrAmbiguous):
		return Result{}, fmt.Errorf("%w: %w", ErrUnsafe, err)
	default:
		return Result{}, fmt.Errorf("read artifact metadata: %w", err)
	}

	return parse(format, filename, parts)
}

// jvmFormat maps sbt onto the maven extractor (the manifest package reads .pom
// and .jar of both the same way).
func jvmFormat(format string) string {
	if format == "sbt" {
		return "maven"
	}

	return format
}

// Unsafe reports whether err means the artifact must be refused.
func Unsafe(err error) bool { return errors.Is(err, ErrUnsafe) }

func parse(format, filename string, parts []manifest.Part) (Result, error) {
	switch format {
	case "pypi":
		return parsePyPI(parts[0].Body), nil
	case "npm":
		return parsePackageJSON([]byte(parts[0].Body))
	case "cargo":
		return parseCargo(parts[0].Body)
	case "nuget":
		return parseNuspec(parts[0].Body)
	case "maven", "sbt":
		return parsePOM(filename, parts)
	}

	return Result{}, ErrUnsupported
}

// base is the file name of an artifact path.
func base(filename string) string {
	return strings.ToLower(path.Base(strings.ReplaceAll(filename, "\\", "/")))
}
