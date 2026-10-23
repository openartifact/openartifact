// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// quarantineAction is the action statement of a vulnerability that is affected
// because its artifact is quarantined.
const quarantineAction = "The artifact is quarantined in OpenArtifact and its downloads are blocked until an administrator approves it."

// Finding is one vulnerability of an artifact with the VEX status the exports
// publish for it.
type Finding struct {
	Vuln          models.Vulnerability
	Status        string // models.VEX* status
	Justification string // OpenVEX justification; only for not_affected
	Impact        string
	Action        string
	// Decided is true when an admin recorded the status; false when it is derived.
	Decided   bool
	UpdatedAt time.Time
}

// Findings lists the vulnerabilities of the artifact with their VEX status:
//
//   - an admin decision (vex_statements) always wins; "not_affected" exists
//     only through such a decision, never from a scan;
//   - otherwise "fixed" when the scanner's fixed_in is at or below the
//     artifact's version;
//   - otherwise "affected" when the artifact is quarantined by its
//     vulnerability scan;
//   - otherwise "under_investigation".
//
// Decisions about vulnerabilities the latest scan does not report are listed
// too (with only the id known). Order follows the scan, then the extra decisions.
func (in *Input) Findings() []Finding {
	var (
		out  []Finding
		seen = map[string]struct{}{}
	)

	scanTime := time.Time{}

	if in.Scan != nil {
		scanTime = in.Scan.UpdatedAt
		if in.Scan.ScannedAt != nil {
			scanTime = *in.Scan.ScannedAt
		}

		for _, v := range in.Scan.Vulnerabilities {
			key := strings.ToUpper(v.ID)
			if v.ID == "" {
				continue
			}

			if _, dup := seen[key]; dup {
				continue
			}

			seen[key] = struct{}{}

			out = append(out, in.finding(v, scanTime))
		}
	}

	// Remaining decisions, in a stable order.
	extra := make([]string, 0, len(in.Decisions))

	for key := range in.Decisions {
		if _, ok := seen[key]; !ok {
			extra = append(extra, key)
		}
	}

	slices.Sort(extra)

	for _, key := range extra {
		out = append(out, in.finding(models.Vulnerability{ID: in.Decisions[key].VulnID}, scanTime))
	}

	return out
}

func (in *Input) finding(v models.Vulnerability, scanTime time.Time) Finding {
	f := Finding{Vuln: v, UpdatedAt: scanTime}

	if d, ok := in.Decisions[strings.ToUpper(v.ID)]; ok {
		f.Status = d.Status
		f.Justification = d.Justification
		f.Impact = d.ImpactStatement
		f.Action = d.ActionStatement
		f.Decided = true
		f.UpdatedAt = d.UpdatedAt

		if f.Status == models.VEXAffected && f.Action == "" {
			f.Action = defaultAction(v, in.Quarantined)
		}

		return f
	}

	switch {
	case v.FixedIn != "" && fixedByAny(in.Format, in.Version, v.FixedIn):
		f.Status = models.VEXFixed
	case in.Quarantined:
		f.Status = models.VEXAffected
		f.Action = defaultAction(v, true)
	default:
		f.Status = models.VEXUnderInvestigation
	}

	return f
}

// defaultAction is the action statement of an affected finding the admin did
// not word: the quarantine notice, the upgrade target or a generic request.
func defaultAction(v models.Vulnerability, quarantined bool) string {
	switch {
	case quarantined:
		return quarantineAction
	case v.FixedIn != "":
		return "Upgrade to " + v.FixedIn + " or later."
	default:
		return "No fixed version is known; apply a vendor mitigation or stop using the artifact."
	}
}
