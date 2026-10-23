// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"slices"
	"testing"
)

func TestDebDistro(t *testing.T) {
	tests := []struct {
		name, version, suite, want string
	}{
		{"debian security update", "3.0.11-1~deb12u2", "", "debian-12"},
		{"debian point update with plus", "1.2.3-1+deb11u1", "", "debian-11"},
		{"debian two digit release", "1.0-1+deb10u12", "", "debian-10"},
		{"debian backport", "6.1.4~bpo12+1", "", "debian-12"},
		{"ubuntu update", "3.0.2-0ubuntu0.22.04.1", "", "ubuntu-22.04"},
		{"ubuntu old release", "1.0.1f-1ubuntu2.27", "", ""},
		{"ubuntu backport", "1.2-3~20.04.1", "", "ubuntu-20.04"},
		{"ubuntu dotted backport", "1.2-3~18.04.1.2", "", "ubuntu-18.04"},
		{"version tag wins over the suite", "3.0.11-1~deb12u2", "bullseye", "debian-12"},
		{"plain version, codename suite", "3.0.11-1", "bookworm", "debian-12"},
		{"plain version, ubuntu codename", "3.0.2-0ubuntu1.10", "jammy", "ubuntu-22.04"},
		{"suite pocket", "1.0-1", "bookworm-security", "debian-12"},
		{"suite updates pocket", "1.0-1", "jammy-updates", "ubuntu-22.04"},
		{"suite proposed updates", "1.0-1", "bookworm-proposed-updates", "debian-12"},
		{"older suite with a slash", "1.0-1", "buster/updates", "debian-10"},
		{"suite is case-insensitive", "1.0-1", "Bookworm", "debian-12"},
		{"stable is an alias, not a release", "1.0-1", "stable", ""},
		{"operator chosen name", "1.0-1", "internal", ""},
		{"nothing known", "1.0-1", "", ""},
		{"epoch only", "1:2.0-1", "", ""},
		{"deb in a word is not a tag", "1.0-1debug12u1", "", ""},
		{"tag needs the update number", "1.0~deb12", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DebDistro(tt.version, tt.suite); got != tt.want {
				t.Errorf("DebDistro(%q, %q) = %q, want %q", tt.version, tt.suite, got, tt.want)
			}
		})
	}
}

func TestRPMDistro(t *testing.T) {
	tests := []struct {
		name, version, release, want string
	}{
		{"rhel from the version", "3.0.7-24.el9", "", "rhel-9"},
		{"rhel with epoch", "1:3.0.7-24.el9", "", "rhel-9"},
		{"rhel from the release", "3.0.7", "24.el9", "rhel-9"},
		{"rhel minor update suffix", "3.0.7-24.el9_2.1", "", "rhel-9"},
		{"rhel 10", "1.0-1.el10", "", "rhel-10"},
		{"rhel 7", "1.0-1.el7", "", "rhel-7"},
		{"rhel with a module", "1.0-1.module+el8.4.0+123+abc", "", "rhel-8"},
		{"centos rebuild tag", "1.0-1.el7.centos", "", "rhel-7"},
		{"rocky rebuild tag", "1.0-1.el9.rocky.0.1", "", "rhel-9"},
		{"fedora", "8.2.1-3.fc39", "", "fedora-39"},
		{"fedora with a trailing field", "8.2.1-3.fc39.1", "", "fedora-39"},
		{"amazon linux 2", "8.3.0-1.amzn2.0.8", "", "amzn-2"},
		{"amazon linux 2023", "8.3.0-1.amzn2023.0.4", "", "amzn-2023"},
		{"release wins over the version", "1.el7-2", "3.el9", "rhel-9"},
		{"no distribution tag", "1.0-1", "", ""},
		{"no release at all", "1.0", "", ""},
		{"suse has no tag grype knows", "1.0-150400.1.1", "", ""},
		{"el in a word is not a tag", "1.0-1.kernel9", "", ""},
		{"el needs a number", "1.0-1.el", "", ""},
		{"empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RPMDistro(tt.version, tt.release); got != tt.want {
				t.Errorf("RPMDistro(%q, %q) = %q, want %q", tt.version, tt.release, got, tt.want)
			}
		})
	}
}

// Every distro the derivation returns is one grype accepts, and its vendor
// is the Package URL namespace.
func TestDerivedDistrosAreValid(t *testing.T) {
	for _, d := range debSuites {
		if !validDistro(d) {
			t.Errorf("suite table holds an invalid distro %q", d)
		}
	}

	for _, d := range []string{
		DebDistro("1~deb12u1", ""), DebDistro("0ubuntu0.22.04.1", ""), DebDistro("1~bpo11+1", ""),
		RPMDistro("1-1.el9", ""), RPMDistro("1-1.fc40", ""), RPMDistro("1-1.amzn2023.0.1", ""),
	} {
		if !validDistro(d) {
			t.Errorf("derived distro %q is invalid", d)
		}
	}
}

func TestDistroVendor(t *testing.T) {
	for distro, want := range map[string]string{
		"debian-12":        "debian",
		"ubuntu-22.04":     "ubuntu",
		"rhel-9":           "redhat",
		"rhel-9.2":         "redhat",
		"fedora-39":        "fedora",
		"amzn-2023":        "amzn",
		"opensuse-leap-15": "opensuse-leap",
	} {
		if got := distroVendor(distro); got != want {
			t.Errorf("distroVendor(%q) = %q, want %q", distro, got, want)
		}
	}
}

func TestNeedsDistro(t *testing.T) {
	for ecosystem, want := range map[string]bool{
		"deb": true, "rpm": true, "DEB": true, "RPM": true,
		"npm": false, "pypi": false, "maven": false, "docker": false, "": false,
	} {
		if got := NeedsDistro(ecosystem); got != want {
			t.Errorf("NeedsDistro(%q) = %v, want %v", ecosystem, got, want)
		}
	}
}

// grype and trivy are replaced by osv only for a deb or rpm whose distribution is
// unknown; everything else runs as requested.
func TestScannersFor(t *testing.T) {
	tests := []struct {
		name      string
		requested []string
		pkg       Package
		want      []string
	}{
		{"deb with a distro keeps grype", []string{"grype"}, Package{Ecosystem: "deb", Distro: "debian-12"}, []string{"grype"}},
		{"rpm with a distro keeps grype", []string{"grype", "osv"}, Package{Ecosystem: "rpm", Distro: "rhel-9"}, []string{"grype", "osv"}},
		{"deb without a distro goes to osv", []string{"grype"}, Package{Ecosystem: "deb"}, []string{"osv"}},
		{"rpm without a distro goes to osv", []string{"grype"}, Package{Ecosystem: "rpm"}, []string{"osv"}},
		{"unusable distro goes to osv", []string{"grype"}, Package{Ecosystem: "deb", Distro: "bookworm"}, []string{"osv"}},
		{"osv already requested is not duplicated", []string{"grype", "osv"}, Package{Ecosystem: "deb"}, []string{"osv"}},
		{"trivy and grype without a distro go to osv", []string{"grype", "trivy"}, Package{Ecosystem: "rpm"}, []string{"osv"}},
		{"trivy alone without a distro goes to osv", []string{"trivy"}, Package{Ecosystem: "deb"}, []string{"osv"}},
		{"trivy with a supported distro stays", []string{"trivy"}, Package{Ecosystem: "deb", Distro: "ubuntu-22.04"}, []string{"trivy"}},
		{"trivy with amazon linux stays", []string{"trivy"}, Package{Ecosystem: "rpm", Distro: "amzn-2023"}, []string{"trivy"}},
		{"trivy has no fedora advisories", []string{"trivy"}, Package{Ecosystem: "rpm", Distro: "fedora-39"}, []string{"osv"}},
		{"fedora keeps grype and replaces trivy", []string{"grype", "trivy"}, Package{Ecosystem: "rpm", Distro: "fedora-39"}, []string{"grype", "osv"}},
		{"result is sorted", []string{"grype", "osv", "trivy"}, Package{Ecosystem: "rpm"}, []string{"osv"}},
		{"without grype and trivy nothing changes", []string{"osv"}, Package{Ecosystem: "deb"}, []string{"osv"}},
		{"trivy on npm stays", []string{"trivy"}, Package{Ecosystem: "npm"}, []string{"trivy"}},
		{"npm keeps grype", []string{"grype"}, Package{Ecosystem: "npm"}, []string{"grype"}},
		{"ecosystem case does not matter", []string{"grype"}, Package{Ecosystem: "DEB"}, []string{"osv"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requested := slices.Clone(tt.requested)

			if got := ScannersFor(requested, tt.pkg); !slices.Equal(got, tt.want) {
				t.Errorf("ScannersFor(%v, %+v) = %v, want %v", tt.requested, tt.pkg, got, tt.want)
			}

			if !slices.Equal(requested, tt.requested) {
				t.Errorf("ScannersFor modified its input: %v, was %v", requested, tt.requested)
			}
		})
	}
}
