// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pkgdeps lists the dependencies a stored package file declares: the
// manifest and, where the package ships one, the lockfile (npm package.json and
// shrinkwrap, pypi Requires-Dist, maven pom, Cargo.toml and Cargo.lock, go.mod,
// nuspec, gemspec, Chart.yaml and Chart.lock, conda index.json, deb Depends, rpm
// Requires, conan recipe, ivy.xml).
//
// Package files are untrusted. Every read goes through internal/ai/manifest in
// strict mode, which bounds each member, the decompressed stream and the
// spooled download and refuses an archive that holds two candidates for the
// same manifest. Parsing is bounded as well (MaxDeps).
package pkgdeps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/openartifact/openartifact/internal/ai/manifest"
)

const (
	// memberLimit bounds one manifest or lockfile read from a package. A
	// lockfile of a large application is several MiB.
	memberLimit = 8 << 20

	// MaxDeps bounds the dependencies returned for one package.
	MaxDeps = 10000

	maxFieldLen = 512
)

var (
	// ErrUnsupported means no dependency list can be read from this file.
	ErrUnsupported = errors.New("dependencies cannot be read from this artifact")

	// ErrUnsafe means the package is too large to read safely or holds more
	// than one candidate for its manifest.
	ErrUnsafe = errors.New("artifact manifest cannot be read safely")
)

// Dep is one declared dependency.
type Dep struct {
	// Name is the package name in the ecosystem's own spelling: the npm
	// "@scope/name", the maven "group:artifact", the go module path.
	Name string
	// Version is the exact version when the manifest or lockfile pins one;
	// "" when only a range is declared.
	Version string
	// Range is the declared version constraint, kept when Version is empty.
	Range string
	// License is the license a lockfile records for the package.
	License string
}

// Result is the dependency list of a package.
type Result struct {
	// Deps are the dependencies, deduplicated by name, in a stable order.
	Deps []Dep
	// Namespace is the purl namespace of the package and its dependencies for
	// ecosystems that require one (deb, rpm); "" when it cannot be told.
	Namespace string
}

// Source describes the package file to read.
type Source struct {
	Format   string
	Filename string
	Open     func() (io.ReadCloser, error)
	// Sibling opens another file of the same logical package (conan recipes).
	Sibling func(name string) (io.ReadCloser, error)
}

// Supported reports whether dependencies can be read from a file.
func Supported(format, filename string) bool {
	return manifest.Supported(jvmFormat(format), filename)
}

// Eligible reports whether the file is the one that represents a package in
// its listing; maven keeps pom, jar and sources side by side and only the
// descriptor is read.
func Eligible(format, filename string) bool {
	return manifest.AutoEligible(format, filename) && manifest.Supported(jvmFormat(format), filename)
}

func jvmFormat(format string) string {
	if format == "sbt" {
		return "maven"
	}

	return format
}

// Read returns the dependencies the package file declares.
//
// Errors: ErrUnsupported, ErrUnsafe, manifest.ErrNotFound (the archive holds
// no manifest; callers treat it as "no dependencies"), or an I/O or parse
// failure.
func Read(ctx context.Context, src Source) (Result, error) {
	format := jvmFormat(src.Format)
	if !manifest.Supported(format, src.Filename) {
		return Result{}, ErrUnsupported
	}

	parts, err := manifest.ExtractParts(ctx, manifest.Source{
		Format:      format,
		Filename:    src.Filename,
		Open:        src.Open,
		Sibling:     src.Sibling,
		MemberLimit: memberLimit,
		Strict:      true,
		Deps:        true,
	})

	switch {
	case err == nil:
	case errors.Is(err, manifest.ErrTooLarge), errors.Is(err, manifest.ErrAmbiguous):
		return Result{}, fmt.Errorf("%w: %w", ErrUnsafe, err)
	case errors.Is(err, manifest.ErrUnsupported):
		return Result{}, ErrUnsupported
	default:
		return Result{}, fmt.Errorf("read package manifest: %w", err)
	}

	res, err := parse(format, src.Filename, parts)
	if err != nil {
		return Result{}, err
	}

	res.Deps = finish(res.Deps)

	return res, nil
}

func parse(format, filename string, parts []manifest.Part) (Result, error) {
	switch format {
	case "npm":
		return parseNPM(parts)
	case "pypi":
		return parsePyPI(parts), nil
	case "maven", "ivy", "sbt":
		return parseJVM(filename, parts)
	case "nuget":
		return parseNuGet(parts)
	case "cargo":
		return parseCargo(parts)
	case "helm":
		return parseHelm(parts)
	case "deb":
		return parseDeb(parts), nil
	case "rpm":
		return parseRPM(parts), nil
	case "go":
		return parseGoMod(filename, parts)
	case "rubygems":
		return parseGem(parts)
	case "conda":
		return parseConda(parts)
	case "conan":
		return parseConan(parts), nil
	}

	return Result{}, ErrUnsupported
}

// part returns the body of the part with the given label.
func part(parts []manifest.Part, label string) (string, bool) {
	for _, p := range parts {
		if p.Label == label {
			return p.Body, true
		}
	}

	return "", false
}

// finish drops unnamed entries, bounds field lengths, deduplicates by name
// (an entry with an exact version beats one with only a range) and sorts.
func finish(in []Dep) []Dep {
	byName := make(map[string]int, len(in))
	out := make([]Dep, 0, len(in))

	for _, d := range in {
		d.Name = clip(strings.TrimSpace(d.Name))
		d.Version = clip(strings.TrimSpace(d.Version))
		d.Range = clip(strings.TrimSpace(d.Range))
		d.License = clip(strings.TrimSpace(d.License))

		if d.Name == "" {
			continue
		}

		if i, dup := byName[d.Name]; dup {
			if out[i].Version == "" && d.Version != "" {
				if d.Range == "" {
					d.Range = out[i].Range
				}

				if d.License == "" {
					d.License = out[i].License
				}

				out[i] = d
			}

			continue
		}

		if len(out) >= MaxDeps {
			break
		}

		byName[d.Name] = len(out)
		out = append(out, d)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

func clip(s string) string {
	if len(s) > maxFieldLen {
		s = strings.ToValidUTF8(s[:maxFieldLen], "")
	}

	return s
}

// merge combines a manifest list with a lockfile list: a locked package takes
// the locked (exact) version and keeps the manifest's range; packages only the
// lockfile knows (transitive dependencies) are appended.
func merge(manifestDeps, locked []Dep) []Dep {
	if len(locked) == 0 {
		return manifestDeps
	}

	ranges := make(map[string]string, len(manifestDeps))
	for _, d := range manifestDeps {
		ranges[d.Name] = d.Range
	}

	out := make([]Dep, 0, len(locked)+len(manifestDeps))

	seen := make(map[string]bool, len(locked))

	for _, d := range locked {
		if r := ranges[d.Name]; r != "" && d.Range == "" {
			d.Range = r
		}

		seen[d.Name] = true

		out = append(out, d)
	}

	for _, d := range manifestDeps {
		if !seen[d.Name] {
			out = append(out, d)
		}
	}

	return out
}

var exactSemver = regexp.MustCompile(`^[=v]?\s*(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)$`)

// exactVersion returns the version a constraint pins ("1.2.3", "=1.2.3",
// "v1.2.3") and "" for anything that is a range.
func exactVersion(spec string) string {
	if m := exactSemver.FindStringSubmatch(strings.TrimSpace(spec)); m != nil {
		return m[1]
	}

	return ""
}

// versionOrRange splits a spec into the pinned version or the range.
func versionOrRange(spec string) (string, string) {
	spec = strings.TrimSpace(spec)
	if v := exactVersion(spec); v != "" {
		return v, ""
	}

	return "", spec
}
