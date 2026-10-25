// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"strconv"
	"strings"
)

// Sense flag bits as defined by rpm (RPMSENSE_*).
const (
	SENSE_LESS         uint32 = 1 << 1
	SENSE_GREATER      uint32 = 1 << 2
	SENSE_EQUAL        uint32 = 1 << 3
	SENSE_ANY          uint32 = 0
	SENSE_LESSEQUAL    uint32 = SENSE_LESS | SENSE_EQUAL
	SENSE_GREATEREQUAL uint32 = SENSE_GREATER | SENSE_EQUAL

	// SENSE_PREREQ, SENSE_SCRIPT_PRE and SENSE_SCRIPT_POST mark requirements
	// that must be satisfied before the package scriptlets run (createrepo_c
	// emits pre="1" for them).
	SENSE_PREREQ      uint32 = 1 << 6
	SENSE_SCRIPT_PRE  uint32 = 1 << 9
	SENSE_SCRIPT_POST uint32 = 1 << 10
	// SENSE_RPMLIB marks rpmlib(...) feature requirements, which createrepo_c
	// leaves out of primary.xml.
	SENSE_RPMLIB uint32 = 1 << 24

	senseCompareMask = SENSE_LESS | SENSE_GREATER | SENSE_EQUAL
)

type Dependency struct {
	Name    string
	Flags   uint32
	Version string
	Epoch   uint32
}

// SenseFlagsString renders the comparison bits of flags as the createrepo_c
// flags attribute value; an unversioned dependency yields "".
func SenseFlagsString(flags uint32) string {
	switch flags & senseCompareMask {
	case SENSE_LESS:
		return "LT"
	case SENSE_GREATER:
		return "GT"
	case SENSE_EQUAL:
		return "EQ"
	case SENSE_LESSEQUAL:
		return "LE"
	case SENSE_GREATEREQUAL:
		return "GE"
	default:
		return ""
	}
}

// IsPre reports whether the requirement must be met before scriptlets run.
func (d Dependency) IsPre() bool {
	return d.Flags&(SENSE_PREREQ|SENSE_SCRIPT_PRE|SENSE_SCRIPT_POST) != 0
}

// IsRPMLib reports whether the dependency is an rpmlib(...) feature requirement.
func (d Dependency) IsRPMLib() bool {
	return d.Flags&SENSE_RPMLIB != 0
}

// SplitVersionRelease splits a dependency version "ver[-rel]" at the last dash.
func SplitVersionRelease(v string) (string, string) {
	if i := strings.LastIndexByte(v, '-'); i >= 0 { //nolint:modernize // CutLast unavailable in supported toolchains
		return v[:i], v[i+1:]
	}

	return v, ""
}

func ParseDependencies(header *Header, nameTag, flagsTag, versionTag TagType) ([]Dependency, error) {
	names, ok := header.GetStringArray(nameTag)
	if !ok || len(names) == 0 {
		return nil, nil
	}

	var flags []int32
	if flagsTag != 0 {
		flags, _ = header.GetInt32s(flagsTag)
	}

	var versions []string
	if versionTag != 0 {
		versions, _ = header.GetStringArray(versionTag)
	}

	deps := make([]Dependency, 0, len(names))
	for i, name := range names {
		dep := Dependency{Name: name}

		if i < len(flags) {
			dep.Flags = uint32(flags[i]) //nolint:gosec // RPM binary format requires specific integer widths
		}

		if i < len(versions) {
			dep.Version = versions[i]
			dep.Epoch, dep.Version = extractVersionEpoch(versions[i])
		}

		deps = append(deps, dep)
	}

	return deps, nil
}

func extractVersionEpoch(ver string) (uint32, string) {
	if before, after, ok := strings.Cut(ver, ":"); ok {
		e, err := strconv.ParseUint(before, 10, 32)
		if err == nil {
			return uint32(e), after
		}
	}

	return 0, ver
}

// Versioned reports whether the dependency carries a version comparison.
func (d Dependency) Versioned() bool {
	return d.Flags&senseCompareMask != 0
}
