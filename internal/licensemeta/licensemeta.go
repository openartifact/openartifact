// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package licensemeta reads the licenses a package declares from the package
// file itself: the metadata inside the artifact (a wheel's METADATA, an npm
// tarball's package.json, a crate's Cargo.toml, a nupkg's .nuspec, a gem's
// gemspec, a .pom or an ivy.xml).
//
// Upload requests carry their own claims about a package (twine's form fields,
// cargo's publish metadata, npm's publish JSON); a client that cannot or will
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
	"github.com/openartifact/openartifact/internal/registry"
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
	// "License", "classifier", "package.json", "Cargo.toml", "composer.json", ".nuspec",
	// "gemspec", "pom" or "ivy.xml".
	Source string
	// Parent is the <parent> a Maven pom declares; set only for a pom that
	// declares no license of its own, whose licenses are then the parent's
	// (see InheritPOMLicenses). Nil otherwise.
	Parent *pkgmaven.Parent
}

// Empty reports whether the artifact declares no license.
func (r Result) Empty() bool { return len(r.Licenses) == 0 }

// Hooks is what the package needs to know about one format to read its
// licenses. Each format has one exported value (NPMHooks, PyPIHooks, ...)
// defined next to its parser and handed to Register by internal/format/plugin;
// a format with no value declares no licenses this package reads, and Supported
// and Expected report false for it.
type Hooks struct {
	// ManifestFormat is the format whose manifest rule extracts the metadata
	// file; empty means the format itself. sbt reads like maven.
	ManifestFormat string
	// Filter narrows the files the manifest rule accepts: a file is supported
	// only when the rule accepts it and Filter (when set) does too. Maven and
	// sbt leave out the .xml files their rule takes for ivy, ivy keeps only its
	// module descriptors.
	Filter func(filename string) bool
	// Expected reports whether a file of the format is a package that declares
	// its own license metadata; supported is what Supported says about the file.
	Expected func(filename string, supported bool) bool
	// MemberLimit bounds one metadata file read from an artifact; zero means
	// memberLimit.
	MemberLimit int64
	// Parse turns the extracted metadata files into the declared licenses.
	// Unused when Read is set.
	Parse func(filename string, parts []manifest.Part) (Result, error)
	// Read reads the licenses of a format whose metadata the manifest package
	// does not extract (rubygems: the gem's own parser); nil means extract with
	// the manifest rule and Parse.
	Read func(ctx context.Context, open func() (io.ReadCloser, error)) (Result, error)
}

var hooks registry.Registry[Hooks] //nolint:gochecknoglobals // the format hook table, filled by Register at startup

// Register adds the hooks of a format. It panics when the format already has
// some; it is meant to run once per format at startup.
func Register(format string, h Hooks) { hooks.Register(format, h) }

// manifestFormat is the format name the manifest package knows the format by.
func (h Hooks) manifestFormat(format string) string {
	if h.ManifestFormat != "" {
		return h.ManifestFormat
	}

	return format
}

// supported reports whether licenses can be read from a file of the format.
func (h Hooks) supported(format, filename string) bool {
	return manifest.Supported(h.manifestFormat(format), filename) && (h.Filter == nil || h.Filter(filename))
}

// Supported reports whether licenses can be read from a file of this format.
func Supported(format, filename string) bool {
	h, ok := hooks.Lookup(format)

	return ok && h.supported(format, filename)
}

// Expected reports whether a file of this format is a package that declares
// its own license metadata: a wheel or sdist, an npm tarball, a crate, a nupkg,
// a gem, a .pom or an ivy.xml. For these a failure to read the licenses (an unsupported
// file type such as a PyPI .egg, a damaged archive) leaves nothing but the
// client's claims to judge the package by. Maven jars, checksums and
// maven-metadata.xml are not: the .pom beside them is the authority, as the
// module descriptor is for the artifacts of an Ivy module. Every format
// Expected holds is also Supported.
func Expected(format, filename string) bool {
	h, ok := hooks.Lookup(format)

	return ok && h.Expected != nil && h.Expected(filename, h.supported(format, filename))
}

// Read reads the licenses of an artifact; open returns a fresh reader over its
// bytes and is called once per attempt. The error is ErrUnsupported,
// ErrNoMetadata, ErrUnsafe, or an I/O or parse failure (the file is not the
// archive its name says, the metadata is not well formed).
func Read(ctx context.Context, format, filename string, open func() (io.ReadCloser, error)) (Result, error) {
	h, ok := hooks.Lookup(format)
	if !ok || !h.supported(format, filename) {
		return Result{}, ErrUnsupported
	}

	if h.Read != nil {
		return h.Read(ctx, open)
	}

	limit := h.MemberLimit
	if limit == 0 {
		limit = memberLimit
	}

	parts, err := manifest.ExtractParts(ctx, manifest.Source{
		Format:      h.manifestFormat(format),
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

	return h.Parse(filename, parts)
}

// Unsafe reports whether err means the artifact must be refused.
func Unsafe(err error) bool { return errors.Is(err, ErrUnsafe) }

// expectAlways is the Expected rule of a format whose every package file
// declares its own license metadata.
func expectAlways(string, bool) bool { return true }

// expectSupported is the Expected rule of a format whose supported files are
// exactly the packages that declare their own license metadata.
func expectSupported(_ string, supported bool) bool { return supported }

// base is the file name of an artifact path.
func base(filename string) string {
	return strings.ToLower(path.Base(strings.ReplaceAll(filename, "\\", "/")))
}
