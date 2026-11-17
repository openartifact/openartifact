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

	"github.com/openartifact/openartifact/pkg/pyversion"
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
	case "npm", "composer", "nuget", "conda", "ansible", "conan", "huggingface":
		return strings.ToLower(name)
	default:
		return name
	}
}

// globRunes reports whether t matches pattern p: "*" matches any run of
// characters (including none, "/" and ":"), "?" exactly one character, every
// other character itself. Matching is case-sensitive and runs in
// O(len(p) * len(t)) at worst without recursion, so a hostile pattern cannot
// blow up. It takes runes so Set.Evaluate converts each pattern once at NewSet
// and the subject once per call.
func globRunes(p, t []rune) bool {
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

// matchesAny reports whether pattern matches one of forms.
func matchesAny(pattern []rune, forms [][]rune) bool {
	for _, f := range forms {
		if globRunes(pattern, f) {
			return true
		}
	}

	return false
}

// versionForms is version as a rule's version pattern is matched against it:
// the version itself and, for the formats whose clients treat several
// spellings as one version, the others they do.
//
// pypi (PEP 440): pip reads "2.0", "2.0.0.0", "02.0.0", "v2.0.0" and "2.0.0" as
// one version, so a file name may spell a release any of these ways; and
// "==2.0.0" also selects the local build "2.0.0+anything" (preferring it). The
// forms are the spelling, its lowercase (PEP 440 ignores case), the canonical
// form (pyversion.Canonical: "2" for all of the above) and, for a local
// version, the canonical public version ("2" for "2.0.0+cpu"). A plain
// version pattern is canonical too (versionPattern), so it matches any
// spelling; a glob is matched as written, against the spelling and the
// canonical form (see versionPattern).
func versionForms(format, version string) [][]rune {
	forms := [][]rune{[]rune(version)}

	if format != "pypi" {
		return forms
	}

	add := func(f string) {
		for _, have := range forms {
			if string(have) == f {
				return
			}
		}

		forms = append(forms, []rune(f))
	}

	add(strings.ToLower(version))

	if pyversion.Valid(version) {
		canonical := pyversion.Canonical(version)
		add(canonical)

		if public, _, local := strings.Cut(canonical, "+"); local {
			add(public)
		}
	}

	return forms
}

// versionPattern is a rule's version pattern as it is matched. pypi: a plain
// PEP 440 version (no "*" or "?") is put in canonical form, so the rule "2.0.0"
// matches every spelling of that release ("2.0", "v2.0.0", "2.0.0+cpu"); a
// glob is lowercased (PEP 440 ignores case) and otherwise kept: it is matched
// against the spelling and the canonical form, and the canonical form drops
// trailing zeros, so "2.0.*" matches "2.0.0" and "2.0.5" through their
// spelling but not a release spelled "2.0" (canonical "2"). A rule for one
// release is therefore best written as a plain version.
func versionPattern(format, pattern string) string {
	if format != "pypi" {
		return pattern
	}

	if strings.ContainsAny(pattern, "*?") {
		return strings.ToLower(pattern)
	}

	if pyversion.Valid(pattern) {
		return pyversion.Canonical(pattern)
	}

	return pattern
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
	rule    *Rule
	pkg     []rune // normalized package pattern
	version []rune // version pattern; nil matches every version
}

// NewSet builds the Set of a repository of the given format. Rules keep their
// order: when several rules of one action match, the first is the one reported.
func NewSet(format, mode string, rules []Rule) *Set {
	if mode != ModeDenylist && mode != ModeAllowlist {
		return &Set{format: format, mode: ModeOff}
	}

	s := &Set{format: format, mode: mode, rules: make([]compiled, len(rules))}
	for i := range rules {
		c := compiled{rule: &rules[i], pkg: []rune(NormalizePackage(format, rules[i].Package))}
		if rules[i].Version != "" {
			c.version = []rune(versionPattern(format, rules[i].Version))
		}

		s.rules[i] = c
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

	name := []rune(NormalizePackage(s.format, pkg))
	forms := versionForms(s.format, version)

	var blocked, allowed *Rule

	for i := range s.rules {
		c := &s.rules[i]

		if !globRunes(c.pkg, name) || (c.version != nil && !matchesAny(c.version, forms)) {
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
