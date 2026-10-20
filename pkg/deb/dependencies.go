// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Sentinel errors for dependency parsing.
var (
	ErrEmptyDependency      = errors.New("empty dependency string")
	ErrInvalidVersionFormat = errors.New("invalid version constraint")
	ErrInvalidVersionOp     = errors.New("invalid version operator")
)

// Dependency represents a single package dependency.
type Dependency struct {
	Package   string
	Version   string
	Operator  string // "", "<<", "<=", "=", ">=", ">>"
	Arch      string // optional architecture qualifier
	Optional  bool   // true for Recommends/Suggests
	Conflicts bool   // true for Conflicts/Breaks
}

// DependencyGroup represents a group of alternative dependencies (OR).
type DependencyGroup struct {
	Dependencies []*Dependency
}

// DependencyList represents a list of dependency groups (AND).
type DependencyList struct {
	Groups []*DependencyGroup
}

// ParseDependencies parses a Debian dependency string
// Format: pkg1 (>= 1.0) | pkg2, pkg3 (= 2.0)
// Commas separate AND groups, pipes separate OR alternatives.
func ParseDependencies(depStr string) (*DependencyList, error) {
	if depStr == "" {
		return &DependencyList{Groups: make([]*DependencyGroup, 0)}, nil
	}

	depList := &DependencyList{
		Groups: make([]*DependencyGroup, 0),
	}

	// Split by comma (AND groups)
	andGroups := strings.SplitSeq(depStr, ",")

	for andGroup := range andGroups {
		group := &DependencyGroup{
			Dependencies: make([]*Dependency, 0),
		}

		// Split by pipe (OR alternatives)
		orAlts := strings.SplitSeq(andGroup, "|")

		for alt := range orAlts {
			dep, err := parseSingleDependency(strings.TrimSpace(alt))
			if err != nil {
				return nil, err
			}

			group.Dependencies = append(group.Dependencies, dep)
		}

		depList.Groups = append(depList.Groups, group)
	}

	return depList, nil
}

// parseSingleDependency parses a single dependency string
// Format: package-name[:arch] [(operator version)].
func parseSingleDependency(depStr string) (*Dependency, error) {
	if depStr == "" {
		return nil, ErrEmptyDependency
	}

	dep := &Dependency{}

	// First, extract the package name and architecture
	// Format: package-name[:arch] [(operator version)]
	depStr = strings.TrimSpace(depStr)

	// Find the opening parenthesis if it exists
	parenIdx := strings.Index(depStr, "(")

	var pkgPart, versionPart string

	if parenIdx != -1 {
		pkgPart = strings.TrimSpace(depStr[:parenIdx])
		versionPart = strings.TrimSpace(depStr[parenIdx:])
	} else {
		pkgPart = depStr
	}

	// Parse package and architecture
	before, after, ok := strings.Cut(pkgPart, ":")
	if ok {
		dep.Package = before
		dep.Arch = after
	} else {
		dep.Package = pkgPart
	}

	// Parse version constraint if present
	if versionPart != "" {
		versionRegex := regexp.MustCompile(`^\(([<>=]+)\s*([^)]+)\)$`)

		matches := versionRegex.FindStringSubmatch(versionPart)
		if matches != nil {
			dep.Operator = matches[1]
			dep.Version = strings.TrimSpace(matches[2])
		} else {
			return nil, fmt.Errorf("%w: %s", ErrInvalidVersionFormat, versionPart)
		}
	}

	// Validate operator
	validOps := map[string]bool{
		"":   true,
		"<<": true,
		"<=": true,
		"=":  true,
		">=": true,
		">>": true,
	}

	if !validOps[dep.Operator] {
		return nil, fmt.Errorf("%w: %s", ErrInvalidVersionOp, dep.Operator)
	}

	return dep, nil
}

// String returns the string representation of a dependency.
func (d *Dependency) String() string {
	s := d.Package
	if d.Arch != "" {
		s += ":" + d.Arch
	}

	if d.Operator != "" {
		s += " (" + d.Operator + " " + d.Version + ")"
	}

	return s
}

// String returns the string representation of a dependency group.
func (dg *DependencyGroup) String() string {
	parts := make([]string, len(dg.Dependencies))
	for i, dep := range dg.Dependencies {
		parts[i] = dep.String()
	}

	return strings.Join(parts, " | ")
}

// String returns the string representation of a dependency list.
func (dl *DependencyList) String() string {
	parts := make([]string, len(dl.Groups))
	for i, group := range dl.Groups {
		parts[i] = group.String()
	}

	return strings.Join(parts, ", ")
}

// GetPackages returns a list of all package names in the dependency list.
func (dl *DependencyList) GetPackages() []string {
	pkgs := make([]string, 0)
	seen := make(map[string]bool)

	for _, group := range dl.Groups {
		for _, dep := range group.Dependencies {
			if !seen[dep.Package] {
				pkgs = append(pkgs, dep.Package)
				seen[dep.Package] = true
			}
		}
	}

	return pkgs
}

// IsSatisfied checks if a dependency is satisfied by a given package version.
func (d *Dependency) IsSatisfied(version string) bool {
	if d.Operator == "" {
		return true
	}

	cmp := compareVersions(version, d.Version)

	switch d.Operator {
	case "<<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "=":
		return cmp == 0
	case ">=":
		return cmp >= 0
	case ">>":
		return cmp > 0
	default:
		return false
	}
}

// compareVersions compares two Debian version strings using dpkg's version
// comparison algorithm: "[epoch:]upstream-version[-debian-revision]".
// Epoch defaults to 0 when absent. Returns -1 if v1 < v2, 0 if equal, 1 if
// v1 > v2.
//
// Within upstream-version and debian-revision, dpkg's verrevcmp ordering
// applies: '~' sorts before everything, including the end of the string
// (so "1.0~rc1" < "1.0"), digit runs compare numerically after stripping
// leading zeros, and any other non-digit run compares byte-by-byte with
// letters ordered before all other characters.
func compareVersions(v1, v2 string) int {
	if v1 == v2 {
		return 0
	}

	epoch1, upstream1, revision1 := splitDpkgVersion(v1)
	epoch2, upstream2, revision2 := splitDpkgVersion(v2)

	if epoch1 != epoch2 {
		if epoch1 < epoch2 {
			return -1
		}

		return 1
	}

	if c := compareVersionPart(upstream1, upstream2); c != 0 {
		return c
	}

	return compareVersionPart(revision1, revision2)
}

// splitDpkgVersion splits a Debian version string into its epoch (default
// 0 when absent or non-numeric), upstream version, and debian revision
// (empty when there is no '-').
func splitDpkgVersion(v string) (int, string, string) {
	epoch := 0
	rest := v

	if idx := strings.IndexByte(rest, ':'); idx >= 0 {
		if n, err := strconv.Atoi(rest[:idx]); err == nil {
			epoch = n
		}

		rest = rest[idx+1:]
	}

	if idx := strings.LastIndexByte(rest, '-'); idx >= 0 {
		return epoch, rest[:idx], rest[idx+1:]
	}

	return epoch, rest, ""
}

// dpkgOrder returns dpkg's ordering weight for a single byte within a
// version component, per verrevcmp's order(): '~' sorts lowest, digits and
// end-of-string sort next (all equal, since digit runs are compared
// separately), letters sort by ASCII value, and everything else sorts
// above letters.
func dpkgOrder(b byte) int {
	switch {
	case b == '~':
		return -1
	case b == 0 || (b >= '0' && b <= '9'):
		return 0
	case (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z'):
		return int(b)
	default:
		return int(b) + 256
	}
}

// compareVersionPart implements dpkg's verrevcmp for a single version
// component (the upstream version or the debian revision).
func compareVersionPart(a, b string) int {
	for len(a) > 0 || len(b) > 0 {
		for isNonDigit(a) || isNonDigit(b) {
			ac, bc := dpkgOrder(byteAt(a, 0)), dpkgOrder(byteAt(b, 0))
			if ac != bc {
				if ac < bc {
					return -1
				}

				return 1
			}

			a, b = advance(a), advance(b)
		}

		for len(a) > 0 && a[0] == '0' {
			a = a[1:]
		}

		for len(b) > 0 && b[0] == '0' {
			b = b[1:]
		}

		digitsA, digitsB := digitRunLen(a), digitRunLen(b)
		if digitsA != digitsB {
			if digitsA < digitsB {
				return -1
			}

			return 1
		}

		if a[:digitsA] != b[:digitsB] {
			if a[:digitsA] < b[:digitsB] {
				return -1
			}

			return 1
		}

		a, b = a[digitsA:], b[digitsB:]
	}

	return 0
}

// isNonDigit reports whether s has a leading byte that is not a digit.
// An empty string is treated as non-digit so the comparison loop still
// consumes the implicit "end of string" ordering token on the other side.
func isNonDigit(s string) bool {
	return len(s) > 0 && (s[0] < '0' || s[0] > '9')
}

// byteAt returns s[i], or 0 if s is too short (the end-of-string token).
func byteAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}

	return 0
}

// advance drops the leading byte of s, or returns s unchanged if empty.
func advance(s string) string {
	if len(s) == 0 {
		return s
	}

	return s[1:]
}

// digitRunLen returns the length of the leading run of ASCII digits in s.
func digitRunLen(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}

	return i
}
