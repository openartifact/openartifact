// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// ImageEcosystem is the ecosystem of a scan of a container image: the scan
// covers the packages of the image's SBOM, each in its own ecosystem.
const ImageEcosystem = "docker"

// imageWorkers bounds the packages of one image scanned at once.
const imageWorkers = 8

// purlEcosystems maps the Package URL types an image SBOM names to the
// ecosystem (repository format) whose scanner mapping, OSV ecosystem and
// version ordering the scan reuses: npm, PyPI, Maven, crates.io, RubyGems,
// NuGet, Go and Packagist (OSV, answered by the local mirror where it holds the
// ecosystem) and, for the operating system packages, Debian and Red Hat through
// osv, or grype and trivy when the release is known. pkg:apk (Alpine) and every
// other type have no scanner mapping and are not scanned.
//
//nolint:gochecknoglobals // constant table
var purlEcosystems = map[string]string{
	"npm":      "npm",
	"pypi":     "pypi",
	"golang":   "go",
	"maven":    "maven",
	"cargo":    "cargo",
	"gem":      "rubygems",
	"nuget":    "nuget",
	"composer": "composer",
	"deb":      "deb",
	"rpm":      "rpm",
}

// PackageFromPURL maps the Package URL of an SBOM component to the package a
// scan looks up. ok is false for a malformed Package URL, one without a
// version, and a type no scanner covers (see purlEcosystems).
func PackageFromPURL(purl string) (Package, bool) {
	typ, namespace, name, version, quals, ok := parsePURL(purl)
	if !ok || version == "" || name == "" {
		return Package{}, false
	}

	eco, ok := purlEcosystems[typ]
	if !ok {
		return Package{}, false
	}

	pkg := Package{Name: name, Version: version, Ecosystem: eco}

	switch typ {
	case "npm", "composer", "golang":
		if len(namespace) > 0 {
			pkg.Name = strings.Join(namespace, "/") + "/" + name
		}
	case "maven":
		if len(namespace) == 0 {
			return Package{}, false
		}

		pkg.Name = strings.Join(namespace, ".") + ":" + name
	case "pypi":
		pkg.Name = strings.ReplaceAll(strings.ToLower(name), "_", "-")
	case "deb":
		pkg.Distro = firstValid(quals.Get("distro"), DebDistro(version, ""))
	case "rpm":
		pkg.Distro = firstValid(rpmQualifierDistro(quals.Get("distro"), namespace), RPMDistro(version, ""))

		if e, err := strconv.Atoi(quals.Get("epoch")); err == nil && e > 0 {
			pkg.Epoch = e
		}
	}

	return pkg, true
}

// firstValid returns the first of candidates that is a valid distro, or "".
func firstValid(candidates ...string) string {
	for _, c := range candidates {
		if validDistro(c) {
			return c
		}
	}

	return ""
}

// rpmQualifierDistro returns the distro qualifier of an rpm Package URL with
// the vendor spelling grype uses ("redhat" is "rhel"); a qualifier that is not
// "<id>-<version>" is completed from the namespace when it is a bare version.
func rpmQualifierDistro(distro string, namespace []string) string {
	distro = strings.ToLower(distro)

	if v, ok := strings.CutPrefix(distro, "redhat-"); ok {
		distro = "rhel-" + v
	}

	if distro != "" && len(namespace) > 0 && !strings.Contains(distro, "-") {
		return strings.ToLower(namespace[0]) + "-" + distro
	}

	return distro
}

// parsePURL splits "pkg:type/ns/name@version?q=v#subpath" into its parts, the
// percent-escapes decoded. ok is false when it has no type or name.
func parsePURL(s string) (string, []string, string, string, url.Values, bool) {
	rest, found := strings.CutPrefix(s, "pkg:")
	if !found {
		return "", nil, "", "", nil, false
	}

	rest, _, _ = strings.Cut(rest, "#")

	rest, query, _ := strings.Cut(rest, "?")

	quals, err := url.ParseQuery(query)
	if err != nil {
		return "", nil, "", "", nil, false
	}

	rest = strings.TrimLeft(rest, "/")

	var typ, version string

	typ, rest, found = strings.Cut(rest, "/")
	if !found || typ == "" {
		return "", nil, "", "", nil, false
	}

	typ = strings.ToLower(typ)

	if i := strings.LastIndex(rest, "@"); i > 0 {
		v, err := url.PathUnescape(rest[i+1:])
		if err != nil {
			return "", nil, "", "", nil, false
		}

		rest, version = rest[:i], v
	}

	segs := strings.Split(rest, "/")

	for i, seg := range segs {
		d, err := url.PathUnescape(seg)
		if err != nil || d == "" {
			return "", nil, "", "", nil, false
		}

		segs[i] = d
	}

	return typ, segs[:len(segs)-1], segs[len(segs)-1], version, quals, true
}

// ImageOutcome is the combined result of scanning the packages of an image.
type ImageOutcome struct {
	Outcome

	// Scanned is the number of distinct packages looked up.
	Scanned int
	// Skipped counts the components no scanner covers (no Package URL or a
	// type without a scanner mapping), by Package URL type ("none" for a
	// component without one).
	Skipped map[string]int
}

// SkippedNote is the client-safe sentence naming the skipped components, ""
// when none was skipped.
func (o ImageOutcome) SkippedNote() string {
	if len(o.Skipped) == 0 {
		return ""
	}

	types := make([]string, 0, len(o.Skipped))
	for t := range o.Skipped {
		types = append(types, t)
	}

	slices.Sort(types)

	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, strconv.Itoa(o.Skipped[t])+" "+t)
	}

	return "not scanned (no scanner covers them): " + strings.Join(parts, ", ")
}

// RunComponents scans every package of the SBOM components (named by their Package URLs, "" for a component without one) with the requested
// scanners (routed per package like a single-package scan) and combines the
// findings, each stamped with its package. The status is failed when no package
// could be scanned by any scanner, partial when some lookups failed, completed
// otherwise; components no scanner covers are counted in Skipped and never turn
// into findings.
func RunComponents(
	ctx context.Context, log *logger.Logger, backend func(name string) ScannerBackend, scanners []string, purls []string,
) ImageOutcome {
	pkgs, skipped := imagePackages(purls)
	out := ImageOutcome{Scanned: len(pkgs), Skipped: skipped}

	results := make([]Outcome, len(pkgs))
	jobs := make(chan int)

	var wg sync.WaitGroup

	for range min(imageWorkers, max(len(pkgs), 1)) {
		wg.Go(func() {
			for i := range jobs {
				names := ScannersFor(scanners, pkgs[i])
				backends := make([]ScannerBackend, 0, len(names))

				for _, n := range names {
					backends = append(backends, backend(n))
				}

				results[i] = Run(ctx, log, backends, pkgs[i])
			}
		})
	}

	for i := range pkgs {
		jobs <- i
	}

	close(jobs)
	wg.Wait()

	out.Outcome = combineImage(pkgs, results)

	return out
}

// imagePackages maps the components to distinct packages, in order.
func imagePackages(purls []string) ([]Package, map[string]int) {
	var (
		pkgs    []Package
		skipped = map[string]int{}
		seen    = map[Package]bool{}
	)

	for _, purl := range purls {
		pkg, ok := PackageFromPURL(purl)
		if !ok {
			typ, _, _, _, _, parsed := parsePURL(purl)
			if !parsed {
				typ = "none"
			}

			skipped[typ]++

			continue
		}

		if !seen[pkg] {
			seen[pkg] = true

			pkgs = append(pkgs, pkg)
		}
	}

	return pkgs, skipped
}

// combineImage merges the per-package outcomes of an image scan.
func combineImage(pkgs []Package, results []Outcome) Outcome {
	out := Outcome{Status: StatusCompleted, Vulnerabilities: []models.Vulnerability{}}
	failedScanners := map[string]*ScanError{}
	failedPkgs := 0

	for i, res := range results {
		for _, v := range res.Vulnerabilities {
			v.Package = pkgs[i].Name + "@" + pkgs[i].Version
			out.Vulnerabilities = append(out.Vulnerabilities, v)
		}

		if res.Status == StatusFailed {
			failedPkgs++
		}

		for _, f := range res.Failures {
			if _, ok := failedScanners[f.Scanner]; !ok {
				failedScanners[f.Scanner] = f
			}
		}
	}

	for _, f := range failedScanners {
		out.Failures = append(out.Failures, f)
	}

	slices.SortFunc(out.Failures, func(a, b *ScanError) int { return strings.Compare(a.Scanner, b.Scanner) })

	switch {
	case len(pkgs) > 0 && failedPkgs == len(pkgs):
		out.Status = StatusFailed
		out.Vulnerabilities = []models.Vulnerability{}
	case len(out.Failures) > 0:
		out.Status = StatusPartial
	}

	return out
}
