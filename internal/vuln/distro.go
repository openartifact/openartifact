// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"regexp"
	"slices"
	"strings"
)

// NeedsDistro reports whether packages of the ecosystem are only meaningful
// inside a distribution release. grype matches a deb or rpm against the
// advisories of one release and, when it is not told which, reports no match
// at all instead of an error.
func NeedsDistro(ecosystem string) bool {
	switch strings.ToLower(ecosystem) {
	case "deb", "rpm":
		return true
	}

	return false
}

// distroPattern is grype's distro qualifier: "<id>-<version>". The version is
// required, "bookworm" alone matches nothing.
var distroPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:-[a-z][a-z0-9_]*)*-[0-9]+(?:\.[0-9]+)*$`)

const maxDistroLen = 64

func validDistro(d string) bool {
	return len(d) <= maxDistroLen && distroPattern.MatchString(d)
}

// distroVendor is the Package URL namespace for a distro: its id, which the
// Package URL specification spells "redhat" for RHEL. The distro must be valid.
func distroVendor(distro string) string {
	id := distro[:strings.LastIndexByte(distro, '-')]
	if id == "rhel" {
		return "redhat"
	}

	return id
}

// ScannersFor returns the scanners that run for the package, sorted and
// without duplicates. grype and trivy cannot scan a deb or rpm whose
// distribution is unknown (or, for trivy, one it has no advisories for: fedora),
// and report no match at all instead of an error, so each is replaced by osv,
// which matches them by name and version alone; every other request runs as
// asked. The stored scan names the scanners returned here, so the result says
// what ran.
func ScannersFor(requested []string, pkg Package) []string {
	if !NeedsDistro(pkg.Ecosystem) {
		return requested
	}

	usable := map[string]bool{
		scannerGrype: validDistro(pkg.Distro),
		scannerTrivy: trivySBOMSupports(pkg.Distro),
	}

	if !slices.ContainsFunc(requested, func(n string) bool { ok, known := usable[n]; return known && !ok }) {
		return requested
	}

	out := make([]string, 0, len(requested))

	for _, name := range requested {
		if ok, known := usable[name]; known && !ok {
			name = scannerOSV
		}

		out = append(out, name)
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// debSuites maps the codenames of Debian and Ubuntu releases to grype distros.
//
//nolint:gochecknoglobals // immutable lookup table
var debSuites = map[string]string{
	"stretch":  "debian-9",
	"buster":   "debian-10",
	"bullseye": "debian-11",
	"bookworm": "debian-12",
	"trixie":   "debian-13",
	"forky":    "debian-14",

	"trusty":   "ubuntu-14.04",
	"xenial":   "ubuntu-16.04",
	"bionic":   "ubuntu-18.04",
	"focal":    "ubuntu-20.04",
	"jammy":    "ubuntu-22.04",
	"noble":    "ubuntu-24.04",
	"plucky":   "ubuntu-25.04",
	"questing": "ubuntu-25.10",
	"resolute": "ubuntu-26.04",
}

// Release tags that Debian and Ubuntu put into the versions of the packages
// they build for one release: "1.2-1+deb12u1", "1.0~bpo12+1" (Debian
// backports), "1.2-0ubuntu0.22.04.1" and "1.2-3~22.04.1" (Ubuntu updates and
// backports). A plain "1.2-1" or "1.2-0ubuntu1.3" names no release.
var (
	debDebianTag   = regexp.MustCompile(`[~+]deb(\d{1,2})u\d+`)
	debBackportTag = regexp.MustCompile(`~bpo(\d{1,2})\+\d+`)
	debUbuntuTag   = regexp.MustCompile(`ubuntu0\.(\d{2}\.(?:04|10))(?:\.\d+)*`)
	debUbuntuBPTag = regexp.MustCompile(`~(\d{2}\.(?:04|10))(?:\.\d+)*$`)
)

// DebDistro returns the grype distro of a deb package, or "" when it is not
// known. The release a package's version is tagged with wins: it says which
// release the package was built for. Otherwise suite, the name of the repository
// distribution the package is served from, decides when it is a release codename
// ("jammy", "bookworm-security").
func DebDistro(version, suite string) string {
	for _, tag := range []struct {
		re     *regexp.Regexp
		prefix string
	}{
		{debDebianTag, "debian-"},
		{debBackportTag, "debian-"},
		{debUbuntuTag, "ubuntu-"},
		{debUbuntuBPTag, "ubuntu-"},
	} {
		if m := tag.re.FindStringSubmatch(version); m != nil {
			return tag.prefix + m[1]
		}
	}

	// "bookworm-updates", "jammy-security" and Debian's older "buster/updates"
	// are pockets of one release.
	codename, _, _ := strings.Cut(strings.ToLower(suite), "-")
	codename, _, _ = strings.Cut(codename, "/")

	return debSuites[codename]
}

// rpmTags are the distribution tags of an rpm release: "1.el9", "24.el9_2.1",
// "1.module+el8.4.0+123+abc", "2.fc39", "1.amzn2.0.8". The rebuilds of RHEL
// (CentOS, Rocky, AlmaLinux) carry the same ".elN" tag and are covered by
// RHEL's advisories, so ".elN" is the rhel distro.
//
//nolint:gochecknoglobals // compiled once
var rpmTags = []struct {
	re     *regexp.Regexp
	prefix string
}{
	{regexp.MustCompile(`[.+_]el(\d+)(?:[._+]|$)`), "rhel-"},
	{regexp.MustCompile(`\.fc(\d+)(?:\.|$)`), "fedora-"},
	{regexp.MustCompile(`\.amzn(2023|2022|2)(?:[._]|$)`), "amzn-"},
}

// RPMDistro returns the grype distro of an rpm from the distribution tag of
// its release, or "" when there is none. release is the rpm's Release; when it
// is empty the release is taken from version ("version-release", the part after
// the last hyphen: an rpm version cannot contain one).
func RPMDistro(version, release string) string {
	if release == "" {
		if _, rel, found := strings.CutLast(version, "-"); found {
			release = rel
		}
	}

	for _, tag := range rpmTags {
		if m := tag.re.FindStringSubmatch(release); m != nil {
			return tag.prefix + m[1]
		}
	}

	return ""
}
