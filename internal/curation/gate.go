// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package curation

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Sources of a Hold.
const (
	SourceMinAge   = "min_age"
	SourceCuration = "curation"
)

// StampBucket is the granularity of Gate.Stamp: a version that ages past the
// minimum age shows up in a gated document at most this late.
const StampBucket = 5 * time.Minute

// Hold is the reason a version is unavailable.
type Hold struct {
	// Source is SourceMinAge or SourceCuration.
	Source string
	// Detail is the sentence clients and the explain endpoint show.
	Detail string
	// Until is when a minimum-age hold ends (zero otherwise).
	Until time.Time
	// Rule is the curation rule that blocks the version; nil for a minimum-age
	// hold and for a version the allowlist does not list.
	Rule *Rule
}

// Gate is everything that can keep a version of an upstream package from the
// clients of a proxy repository: the curation rules and the minimum age. A
// proxy builds one per request and every format asks it the same question.
// The zero Gate holds nothing back.
type Gate struct {
	// Set is the repository's curation rules; nil or off decides nothing.
	Set *Set
	// MinAge is the repository's minimum age; 0 turns the age check off.
	MinAge time.Duration
	// Now is the time the minimum age is measured at.
	Now time.Time

	tag string // curation part of Stamp, computed by NewGate
}

// NewGate builds the gate of a repository of the given format. mode and rules
// are the repository's curation (rules are ignored when mode is off).
func NewGate(format, mode string, rules []Rule, minAgeHours int, now time.Time) Gate {
	g := Gate{Set: NewSet(format, mode, rules), Now: now}
	if minAgeHours > 0 {
		g.MinAge = time.Duration(minAgeHours) * time.Hour
	}

	if g.Set.Active() {
		h := sha256.New()
		h.Write([]byte(g.Set.Mode()))

		for i := range rules {
			h.Write([]byte("\x00" + rules[i].Action + "\x00" + rules[i].Package + "\x00" + rules[i].Version))
		}

		g.tag = hex.EncodeToString(h.Sum(nil)[:6])
	}

	return g
}

// Active reports whether the gate can hold anything back. A format that has
// nothing to filter or check when it is inactive keeps its plain path.
func (g Gate) Active() bool { return g.MinAge > 0 || g.Set.Active() }

// AgeActive reports whether the minimum age is on, i.e. whether the publish time
// of a version is worth looking up.
func (g Gate) AgeActive() bool { return g.MinAge > 0 }

// Hidden reports whether version of pkg is unavailable and why. published is
// the publish time the upstream states, zero when it does not: such a version
// passes the minimum age (the gate only holds back what it can date). The
// curation rules are asked first; a matching allow rule exempts the version
// from the minimum age.
func (g Gate) Hidden(pkg, version string, published time.Time) (bool, Hold) {
	var v Verdict

	if g.Set.Active() {
		v = g.Set.Evaluate(pkg, version)
		if v.Hidden {
			return true, Hold{Source: SourceCuration, Detail: curationDetail(v.Rule), Rule: v.Rule}
		}
	}

	if g.MinAge > 0 && !v.ExemptMinAge && !published.IsZero() && g.Now.Sub(published) < g.MinAge {
		return true, Hold{
			Source: SourceMinAge,
			Detail: "version is younger than the repository's minimum age",
			Until:  published.Add(g.MinAge),
		}
	}

	return false, Hold{}
}

func curationDetail(rule *Rule) string {
	switch {
	case rule == nil:
		return "not on the repository's allowlist"
	case rule.Reason != "":
		return "blocked by repository curation: " + rule.Reason
	default:
		return "blocked by repository curation"
	}
}

// Stamp is the part of a rendered document's validator that stands for the
// gate: the minimum age with the bucket of Now, and a digest of the curation
// mode and rules. What a gated document lists changes as versions age and as
// rules change, with no change in the cached upstream blob, so the validator of
// the blob alone would revalidate a stale rendering as fresh. "" when the gate
// is inactive: the validator stays what it was.
func (g Gate) Stamp() string {
	var b strings.Builder

	if g.MinAge > 0 {
		b.WriteString("h" + strconv.Itoa(int(g.MinAge/time.Hour)) + "@" + strconv.FormatInt(g.Now.Unix()/int64(StampBucket/time.Second), 10))
	}

	if g.tag != "" {
		b.WriteString("c" + g.tag)
	}

	return b.String()
}
