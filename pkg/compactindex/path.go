// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package compactindex

import (
	"strings"
)

// Kind is the type of a gem server request path.
type Kind int

const (
	// KindVersions is "versions".
	KindVersions Kind = iota
	// KindNames is "names".
	KindNames
	// KindInfo is "info/<gem>".
	KindInfo
	// KindGem is "gems/<name>-<version>[-<platform>].gem".
	KindGem
	// KindGemspec is "quick/Marshal.4.8/<name>-<version>[-<platform>].gemspec.rz".
	KindGemspec
	// KindSpecs is one of the legacy Marshal indexes (specs.4.8.gz,
	// latest_specs.4.8.gz, prerelease_specs.4.8.gz).
	KindSpecs
)

// Path is a parsed gem server request path.
type Path struct {
	Kind Kind
	// Gem is the gem of a KindInfo path.
	Gem string
	// File is the "<name>-<version>[-<platform>]" of a KindGem or KindGemspec
	// path, and the file name of a KindSpecs path.
	File string
}

const (
	gemSuffix     = ".gem"
	gemspecSuffix = ".gemspec.rz"
	quickDir      = "quick/Marshal.4.8/"
)

// ParsePath classifies a repository-relative request path; ok is false for
// anything a gem server does not serve from the root of the index.
func ParsePath(rel string) (Path, bool) {
	rel = strings.TrimPrefix(rel, "/")

	switch rel {
	case "versions":
		return Path{Kind: KindVersions}, true
	case "names":
		return Path{Kind: KindNames}, true
	case "specs.4.8.gz", "latest_specs.4.8.gz", "prerelease_specs.4.8.gz":
		return Path{Kind: KindSpecs, File: rel}, true
	}

	if gem, ok := strings.CutPrefix(rel, "info/"); ok {
		return Path{Kind: KindInfo, Gem: gem}, ValidName(gem)
	}

	if file, ok := strings.CutPrefix(rel, "gems/"); ok {
		if stem, ok := strings.CutSuffix(file, gemSuffix); ok {
			return Path{Kind: KindGem, File: stem}, validFile(stem)
		}

		return Path{}, false
	}

	if file, ok := strings.CutPrefix(rel, quickDir); ok {
		if stem, ok := strings.CutSuffix(file, gemspecSuffix); ok {
			return Path{Kind: KindGemspec, File: stem}, validFile(stem)
		}
	}

	return Path{}, false
}

// validFile reports whether stem can be "<name>-<version>[-<platform>]": name
// characters throughout, and a hyphen followed by a digit somewhere after the
// first byte.
func validFile(stem string) bool {
	return len(SplitFile(stem)) > 0
}

// FileSplit is one reading of a gem file name.
type FileSplit struct {
	// Name is the gem name.
	Name string
	// Version is the info file's first field for the file: the version, then
	// "-<platform>" for a platform gem.
	Version string
}

// SplitFile returns every way "<name>-<version>[-<platform>]" can be read, in
// order of increasing name length. Gem names and platforms may hold hyphens and
// versions never do, so each hyphen followed by a digit is a candidate boundary;
// the candidates of the real gem server are told apart by looking them up.
func SplitFile(stem string) []FileSplit {
	if len(stem) > 2*maxFieldLen+1 {
		return nil
	}

	var out []FileSplit

	for i := 1; i < len(stem)-1; i++ {
		if stem[i] != '-' || stem[i+1] < '0' || stem[i+1] > '9' {
			continue
		}

		name, version := stem[:i], stem[i+1:]
		if ValidName(name) && ValidVersionField(version) {
			out = append(out, FileSplit{Name: name, Version: version})
		}
	}

	return out
}
