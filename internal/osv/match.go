// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package osv

import (
	"encoding/json"
	"slices"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Range is an OSV affected range: the events that open and close the
// vulnerable intervals of a version scheme.
type Range struct {
	Type   string  `json:"type"`
	Events []Event `json:"events"`
}

// Event is one event of a range; exactly one field is set.
type Event struct {
	Introduced   string `json:"introduced,omitempty"`
	Fixed        string `json:"fixed,omitempty"`
	LastAffected string `json:"last_affected,omitempty"`
	Limit        string `json:"limit,omitempty"`
}

// Affects reports whether version of the package an affected entry names is
// affected, and the lowest fixed version above it when a range says so. The
// version is affected when the entry lists it explicitly or a range contains it
// (OSV schema, "Evaluation"). A version the ecosystem's scheme cannot parse is
// only matched by the explicit list.
func Affects(aff models.OSVAffected, version string) (bool, string) {
	var (
		affected bool
		fixed    string
	)

	version = canonicalVersion(aff.Ecosystem, version)

	if slices.ContainsFunc(aff.Versions, func(v string) bool { return canonicalVersion(aff.Ecosystem, v) == version }) {
		affected = true
	}

	var ranges []Range
	if len(aff.Ranges) > 0 {
		if err := json.Unmarshal(aff.Ranges, &ranges); err != nil {
			return affected, ""
		}
	}

	for _, r := range ranges {
		in, fix := r.contains(aff.Ecosystem, version)
		if !in {
			continue
		}

		affected = true

		if fix != "" && (fixed == "" || lowerVersion(aff.Ecosystem, r.Type, fix, fixed)) {
			fixed = fix
		}
	}

	return affected, fixed
}

func lowerVersion(ecosystem, rangeType, a, b string) bool {
	if cmp := comparator(rangeType, ecosystem); cmp != nil {
		if c, ok := cmp(a, b); ok {
			return c < 0
		}
	}

	return false
}

// contains evaluates the range for version: events are walked in version
// order, "introduced" turns the version affected from its value on, "fixed"
// and "last_affected" turn it off (at the value and after it). It returns the
// first fixed version above the queried one when the version is affected.
func (r Range) contains(ecosystem, version string) (bool, string) {
	cmp := comparator(r.Type, ecosystem)
	if cmp == nil {
		return false, ""
	}

	if _, ok := cmp(version, version); !ok {
		return false, ""
	}

	events, ok := r.sorted(cmp)
	if !ok {
		return false, ""
	}

	affected := false

	var fixed string

	for _, e := range events {
		switch {
		case e.Introduced != "":
			if e.Introduced == "0" || atLeast(cmp, version, e.Introduced) {
				affected = true
			}
		case e.Fixed != "":
			if atLeast(cmp, version, e.Fixed) {
				affected, fixed = false, ""
			} else if affected && fixed == "" {
				fixed = e.Fixed
			}
		case e.LastAffected != "":
			if above, ok := cmp(version, e.LastAffected); ok && above > 0 {
				affected, fixed = false, ""
			}
		}
	}

	return affected, fixed
}

func atLeast(cmp compareFunc, version, bound string) bool {
	c, ok := cmp(version, bound)

	return ok && c >= 0
}

// sorted returns the introduced/fixed/last_affected events ordered by version
// (the order the evaluation needs; "0" is below every version). ok is false
// when an event version cannot be ordered.
func (r Range) sorted(cmp compareFunc) ([]Event, bool) {
	events := make([]Event, 0, len(r.Events))

	for _, e := range r.Events {
		if e.Introduced != "" || e.Fixed != "" || e.LastAffected != "" {
			events = append(events, e)
		}
	}

	valid := true

	slices.SortStableFunc(events, func(a, b Event) int {
		va, vb := a.version(), b.version()

		switch {
		case va == "0" && vb == "0":
			return 0
		case va == "0":
			return -1
		case vb == "0":
			return 1
		}

		c, ok := cmp(va, vb)
		if !ok {
			valid = false
		}

		return c
	})

	return events, valid
}

func (e Event) version() string {
	switch {
	case e.Introduced != "":
		return e.Introduced
	case e.Fixed != "":
		return e.Fixed
	default:
		return e.LastAffected
	}
}
