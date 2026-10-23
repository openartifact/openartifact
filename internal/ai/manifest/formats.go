// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/openartifact/openartifact/pkg/deb"
	"github.com/openartifact/openartifact/pkg/rpm"
)

// maxCompressedMember bounds a compressed member that is itself decoded
// (rubygems metadata.gz).
const maxCompressedMember = 1 << 20

// atDepthOne matches <dir>/<file> members named file.
func atDepthOne(file string) func(string) bool {
	return func(name string) bool {
		f, ok := depthOne(name)
		return ok && f == file
	}
}

func extractNPM(ctx context.Context, src Source) ([]section, error) {
	return tarManifest(ctx, src, compGzip, []slot{{label: "package.json", match: atDepthOne("package.json"), unique: true}})
}

func extractCargo(ctx context.Context, src Source) ([]section, error) {
	return tarManifest(ctx, src, compGzip, []slot{{label: "Cargo.toml", match: atDepthOne("Cargo.toml"), unique: true}})
}

func extractHelm(ctx context.Context, src Source) ([]section, error) {
	return tarManifest(ctx, src, compGzip, []slot{
		{label: "Chart.yaml", match: atDepthOne("Chart.yaml")},
		{label: "requirements.yaml", match: atDepthOne("requirements.yaml")},
	})
}

func extractPyPI(ctx context.Context, src Source) ([]section, error) {
	name := strings.ToLower(path.Base(src.Filename))

	switch {
	case hasAnySuffix(name, ".whl", ".zip", ".egg"):
		return zipManifest(ctx, src, []slot{{
			label:  "METADATA",
			decode: readPyText,
			unique: true,
			match: func(n string) bool {
				clean, ok := topLevel(n)
				if !ok {
					return false
				}

				dir, file, found := strings.Cut(clean, "/")
				if !found || strings.Contains(file, "/") {
					return false
				}

				return (strings.HasSuffix(dir, ".dist-info") && file == "METADATA") ||
					(file == "PKG-INFO" && dir != "")
			},
		}})
	case strings.HasSuffix(name, ".tar.bz2"):
		return tarManifest(ctx, src, compBzip2, []slot{{label: "PKG-INFO", decode: readPyText, match: atDepthOne("PKG-INFO"), unique: true}})
	default:
		return tarManifest(ctx, src, compGzip, []slot{{label: "PKG-INFO", decode: readPyText, match: atDepthOne("PKG-INFO"), unique: true}})
	}
}

func extractJVM(ctx context.Context, src Source) ([]section, error) {
	name := strings.ToLower(path.Base(src.Filename))

	if strings.HasSuffix(name, ".jar") {
		return zipManifest(ctx, src, []slot{
			{label: "pom.xml", match: isEmbeddedPOM},
			{label: "META-INF/MANIFEST.MF", match: func(n string) bool { return n == "META-INF/MANIFEST.MF" }},
		})
	}

	return readBlobText(ctx, src)
}

// isEmbeddedPOM matches META-INF/maven/<groupId>/<artifactId>/pom.xml.
func isEmbeddedPOM(name string) bool {
	rest, ok := strings.CutPrefix(name, "META-INF/maven/")
	if !ok {
		return false
	}

	parts := strings.Split(rest, "/")

	return len(parts) == 3 && parts[2] == "pom.xml"
}

func extractNuGet(ctx context.Context, src Source) ([]section, error) {
	return zipManifest(ctx, src, []slot{{
		label:  ".nuspec",
		unique: true,
		match: func(n string) bool {
			f, ok := rootFile(n)
			return ok && hasSuffixFold(f, ".nuspec")
		},
	}})
}

func extractGoMod(ctx context.Context, src Source) ([]section, error) {
	if hasSuffixFold(src.Filename, ".mod") {
		return readBlobText(ctx, src)
	}

	return zipManifest(ctx, src, []slot{{
		label: "go.mod",
		match: func(n string) bool {
			dir, ok := strings.CutSuffix(n, "/go.mod")
			if !ok {
				return false
			}

			at := strings.LastIndex(dir, "@")

			return at >= 0 && !strings.Contains(dir[at:], "/")
		},
	}})
}

func extractGem(ctx context.Context, src Source) ([]section, error) {
	return tarManifest(ctx, src, compNone, []slot{{
		label: "metadata (gemspec)",
		match: func(n string) bool {
			f, ok := rootFile(n)
			return ok && f == "metadata.gz"
		},
		decode: func(r io.Reader, l limits) (string, error) {
			gr, err := gzip.NewReader(io.LimitReader(r, maxCompressedMember))
			if err != nil {
				return "", fmt.Errorf("gzip open: %w", err)
			}
			defer func() { _ = gr.Close() }()

			return readText(gr, l)
		},
	}})
}

func condaInfoSlots() []slot {
	return []slot{
		{label: "info/index.json", match: func(n string) bool { return cleanName(n) == "info/index.json" }},
		{label: "info/about.json", match: func(n string) bool { return cleanName(n) == "info/about.json" }},
	}
}

func cleanName(n string) string {
	c, _ := topLevel(n)
	return c
}

func extractConda(ctx context.Context, src Source) ([]section, error) {
	if !hasSuffixFold(src.Filename, ".conda") {
		return tarManifest(ctx, src, compBzip2, condaInfoSlots())
	}

	// .conda is a zip holding info-<pkg>.tar.zst next to pkg-<pkg>.tar.zst.
	return zipManifest(ctx, src, []slot{{
		label: "",
		match: func(n string) bool {
			f, ok := rootFile(n)
			return ok && strings.HasPrefix(f, "info-") && strings.HasSuffix(f, ".tar.zst")
		},
		decode: func(r io.Reader, l limits) (string, error) {
			stream, closeDec, err := decompress(r, compZstd)
			if err != nil {
				return "", err
			}
			defer closeDec()

			parts, err := collectTar(ctx, stream, condaInfoSlots(), l)
			if err != nil {
				return "", err
			}

			return join(parts...), nil
		},
	}})
}

// conanSiblings are the recipe files worth sending next to the manifest.
func conanSiblings() []string { return []string{"conanfile.py", "conandata.yml"} }

func extractConan(ctx context.Context, src Source) ([]section, error) {
	var parts []section

	if src.Sibling != nil {
		for _, name := range conanSiblings() {
			rc, err := src.Sibling(name)
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", name, err)
			}

			if rc == nil {
				continue
			}

			text, err := readText(ctxReader{ctx: ctx, r: rc}, src.limits())
			_ = rc.Close()

			if err != nil {
				return nil, err
			}

			parts = append(parts, section{label: name, body: text})
		}
	}

	if len(parts) == 0 {
		return readBlobText(ctx, src)
	}

	return parts, nil
}

// debFieldOrder lists the control fields sent first, in this order.
func debFieldOrder() []string {
	return []string{
		"Package", "Version", "Architecture", "Maintainer", "Homepage", "Section", "Priority", "License",
		"Depends", "Pre-Depends", "Recommends", "Suggests", "Provides", "Conflicts", "Breaks", "Replaces",
		"Description",
	}
}

func extractDeb(ctx context.Context, src Source) ([]section, error) {
	ra, err := openAt(ctx, src.Open)
	if err != nil {
		return nil, err
	}
	defer ra.release()

	df, err := deb.ParseDebFileAt(ra, ra.size)
	if err != nil {
		return nil, fmt.Errorf("parse deb: %w", err)
	}

	fields := df.ControlFile.Fields
	seen := make(map[string]bool, len(fields))

	var b strings.Builder

	for _, k := range debFieldOrder() {
		if v, ok := fields[k]; ok {
			fmt.Fprintf(&b, "%s: %s\n", k, v)

			seen[k] = true
		}
	}

	rest := make([]string, 0, len(fields))

	for k := range fields {
		if !seen[k] {
			rest = append(rest, k)
		}
	}

	sort.Strings(rest)

	for _, k := range rest {
		fmt.Fprintf(&b, "%s: %s\n", k, fields[k])
	}

	return []section{{label: "control", body: b.String()}}, nil
}

// maxRPMDeps bounds the dependency lines rendered from an rpm header.
const maxRPMDeps = 200

func extractRPM(ctx context.Context, src Source) ([]section, error) {
	rc, err := src.Open()
	if err != nil {
		return nil, fmt.Errorf("open artifact: %w", err)
	}
	defer func() { _ = rc.Close() }()

	pkg, err := rpm.ParseRPM(ctxReader{ctx: ctx, r: rc})
	if err != nil {
		return nil, fmt.Errorf("parse rpm: %w", err)
	}

	var b strings.Builder

	version := pkg.Version
	if pkg.Release != "" {
		version += "-" + pkg.Release
	}

	fmt.Fprintf(&b, "Name: %s\nVersion: %s\nArchitecture: %s\n", pkg.Name, version, pkg.Arch)

	for _, kv := range [][2]string{
		{"Summary", pkg.Summary}, {"License", pkg.License}, {"URL", pkg.URL},
		{"Vendor", pkg.Vendor}, {"Packager", pkg.Packager}, {"Group", pkg.Group},
		{"Source RPM", pkg.SourceRPM}, {"Description", pkg.Description},
	} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", kv[0], kv[1])
		}
	}

	writeRPMDeps(&b, "Requires", pkg.Requires)
	writeRPMDeps(&b, "Provides", pkg.Provides)
	writeRPMDeps(&b, "Conflicts", pkg.Conflicts)

	return []section{{label: "rpm header", body: b.String()}}, nil
}

func writeRPMDeps(b *strings.Builder, label string, deps []rpm.Dependency) {
	if len(deps) == 0 {
		return
	}

	if len(deps) > maxRPMDeps {
		deps = deps[:maxRPMDeps]
	}

	b.WriteString(label + ":\n")

	for _, d := range deps {
		line := "  " + d.Name

		if op := rpm.SenseFlagsString(d.Flags); op != "" && d.Version != "" {
			line += " " + op + " " + d.Version
		}

		b.WriteString(line + "\n")
	}
}
