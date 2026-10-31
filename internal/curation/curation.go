// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package curation decides which upstream packages and versions a proxy
// repository may serve. A repository has a mode (off, denylist, allowlist) and
// a list of rules, each a block or an allow of a package pattern and an
// optional version pattern. The package pattern is matched against the name
// the format normalizes to (see NormalizePackage), the version pattern against
// the version string the format shows clients.
//
// The package is a leaf: the proxy builds its per-request gate from it, the
// REST API validates rules and explains availability with it.
package curation

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Modes of a repository's curation.
const (
	// ModeOff serves everything the upstream offers (the default); rules are
	// kept but not applied.
	ModeOff = "off"
	// ModeDenylist serves everything except versions a block rule matches.
	ModeDenylist = "denylist"
	// ModeAllowlist serves only versions an allow rule matches (and no block
	// rule blocks).
	ModeAllowlist = "allowlist"
)

// Actions of a rule.
const (
	ActionBlock = "block"
	ActionAllow = "allow"
)

// Limits of a rule's fields, in bytes.
const (
	MaxPackagePattern = 255
	MaxVersionPattern = 128
	MaxReason         = 500
	// MaxRules bounds the rules of one repository: every request of the
	// repository evaluates them.
	MaxRules = 1000
)

// Rule is one curation rule of a repository.
type Rule struct {
	ID           string
	RepositoryID string
	// Action is ActionBlock or ActionAllow.
	Action string
	// Package is a glob ("*" any run of characters, "?" one character) over the
	// normalized package name.
	Package string
	// Version is a glob over the version string; "" matches every version.
	Version   string
	Reason    string
	CreatedBy string
	CreatedAt time.Time
}

// ValidMode reports whether mode is one of the three modes.
func ValidMode(mode string) bool {
	return mode == ModeOff || mode == ModeDenylist || mode == ModeAllowlist
}

// ValidAction reports whether action is ActionBlock or ActionAllow.
func ValidAction(action string) bool { return action == ActionBlock || action == ActionAllow }

// ErrInvalidRule wraps every ValidateRule failure.
var ErrInvalidRule = errors.New("invalid curation rule")

// ValidateRule checks the fields a client supplies: the action, a non-empty
// package pattern, and the length and content of the patterns and the reason.
func ValidateRule(r *Rule) error {
	switch {
	case !ValidAction(r.Action):
		return fmt.Errorf("%w: action must be %q or %q", ErrInvalidRule, ActionBlock, ActionAllow)
	case strings.TrimSpace(r.Package) == "":
		return fmt.Errorf("%w: package pattern is required", ErrInvalidRule)
	case len(r.Package) > MaxPackagePattern:
		return fmt.Errorf("%w: package pattern is longer than %d bytes", ErrInvalidRule, MaxPackagePattern)
	case len(r.Version) > MaxVersionPattern:
		return fmt.Errorf("%w: version pattern is longer than %d bytes", ErrInvalidRule, MaxVersionPattern)
	case len(r.Reason) > MaxReason:
		return fmt.Errorf("%w: reason is longer than %d bytes", ErrInvalidRule, MaxReason)
	case !cleanText(r.Package) || !cleanText(r.Version):
		return fmt.Errorf("%w: patterns must be printable text without spaces", ErrInvalidRule)
	case !utf8.ValidString(r.Reason) || strings.ContainsFunc(r.Reason, isControl):
		return fmt.Errorf("%w: reason must be printable text", ErrInvalidRule)
	}

	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// cleanText is true for UTF-8 without control characters or whitespace.
func cleanText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return isControl(r) || r == ' ' })
}

var separatorRuns = regexp.MustCompile(`[-_.]+`)

// NormalizePackage returns the form of a package name curation rules are
// matched in: PEP 503 for pypi (lower case, runs of "-", "_" and "." become one
// "-"), lower case for the registries that compare names case-insensitively
// (npm, composer, nuget, conda, ansible, conan), lower case with "_" read as "-"
// for cargo, and the name as the format shows it otherwise (go module path,
// maven "group:artifact", helm, rubygems, deb, rpm, ...). Rule patterns are put
// through the same function, so "Django-*" and "django_*" both match "django-rest".
func NormalizePackage(format, name string) string {
	switch format {
	case "pypi":
		return strings.ToLower(separatorRuns.ReplaceAllString(name, "-"))
	case "cargo":
		return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
	case "npm", "composer", "nuget", "conda", "ansible", "conan":
		return strings.ToLower(name)
	default:
		return name
	}
}

// Glob reports whether s matches pattern: "*" matches any run of characters
// (including none, "/" and ":"), "?" exactly one character, every other
// character itself. Matching is case-sensitive and runs in O(len(pattern) *
// len(s)) at worst without recursion, so a hostile pattern cannot blow up.
func Glob(pattern, s string) bool {
	p, t := []rune(pattern), []rune(s)
	pi, ti := 0, 0
	star, mark := -1, 0

	for ti < len(t) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ti
			pi++
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]):
			pi++
			ti++
		case star >= 0:
			mark++
			ti = mark
			pi = star + 1
		default:
			return false
		}
	}

	for pi < len(p) && p[pi] == '*' {
		pi++
	}

	return pi == len(p)
}

// Verdict is what a Set decides for one package version.
type Verdict struct {
	// Hidden: the version is unavailable. Rule is the matching block rule, nil
	// when the allowlist simply does not list the version.
	Hidden bool
	Rule   *Rule
	// ExemptMinAge: an allow rule matched, so the minimum-age gate does not
	// apply to the version.
	ExemptMinAge bool
	// AllowRule is the allow rule that exempted the version.
	AllowRule *Rule
}

// Set is the rules of one repository in one mode, ready to evaluate. The zero
// value and a Set in ModeOff decide nothing.
type Set struct {
	format string
	mode   string
	rules  []compiled
}

type compiled struct {
	rule *Rule
	pkg  string // normalized package pattern
}

// NewSet builds the Set of a repository of the given format. Rules keep their
// order: when several rules of one action match, the first is the one reported.
func NewSet(format, mode string, rules []Rule) *Set {
	if mode != ModeDenylist && mode != ModeAllowlist {
		return &Set{format: format, mode: ModeOff}
	}

	s := &Set{format: format, mode: mode, rules: make([]compiled, len(rules))}
	for i := range rules {
		s.rules[i] = compiled{rule: &rules[i], pkg: NormalizePackage(format, rules[i].Package)}
	}

	return s
}

// Active reports whether the Set decides anything (mode is not off).
func (s *Set) Active() bool { return s != nil && s.mode != ModeOff && s.mode != "" }

// Mode is the mode the Set was built for.
func (s *Set) Mode() string {
	if s == nil || s.mode == "" {
		return ModeOff
	}

	return s.mode
}

// Evaluate decides pkg (any form: it is normalized here) at version. In
// denylist mode a block rule hides the version; in allowlist mode a block rule
// hides it too, and so does the absence of a matching allow rule. A block rule
// beats an allow rule. A matching allow rule exempts the version from the
// minimum-age gate in both modes.
func (s *Set) Evaluate(pkg, version string) Verdict {
	if !s.Active() {
		return Verdict{}
	}

	name := NormalizePackage(s.format, pkg)

	var blocked, allowed *Rule

	for i := range s.rules {
		c := &s.rules[i]

		if !Glob(c.pkg, name) || (c.rule.Version != "" && !Glob(c.rule.Version, version)) {
			continue
		}

		switch c.rule.Action {
		case ActionBlock:
			if blocked == nil {
				blocked = c.rule
			}
		case ActionAllow:
			if allowed == nil {
				allowed = c.rule
			}
		}
	}

	switch {
	case blocked != nil:
		return Verdict{Hidden: true, Rule: blocked}
	case allowed != nil:
		return Verdict{ExemptMinAge: true, AllowRule: allowed}
	case s.mode == ModeAllowlist:
		return Verdict{Hidden: true}
	}

	return Verdict{}
}
