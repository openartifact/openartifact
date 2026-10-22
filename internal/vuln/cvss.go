// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"math"
	"strings"
)

// cvssWeight returns the CVSS v3 weight of metric value v, or false when v is
// not valid for the metric. changed is the scope-changed flag (affects PR).
func cvssWeight(metric, v string, changed bool) (float64, bool) {
	table := map[string]map[string]float64{
		"AV": {"N": 0.85, "A": 0.62, "L": 0.55, "P": 0.2},
		"AC": {"L": 0.77, "H": 0.44},
		"UI": {"N": 0.85, "R": 0.62},
		"C":  {"H": 0.56, "L": 0.22, "N": 0},
		"I":  {"H": 0.56, "L": 0.22, "N": 0},
		"A":  {"H": 0.56, "L": 0.22, "N": 0},
		"PR": {"N": 0.85, "L": 0.62, "H": 0.27},
	}

	if metric == "PR" && changed {
		table["PR"] = map[string]float64{"N": 0.85, "L": 0.68, "H": 0.5}
	}

	w, ok := table[metric][v]

	return w, ok
}

// cvssRoundUp is the CVSS v3.1 Roundup function (spec appendix A), robust to
// floating point error.
func cvssRoundUp(x float64) float64 {
	i := int(math.Round(x * 100000))
	if i%10000 == 0 {
		return float64(i) / 100000
	}

	return (math.Floor(float64(i)/10000) + 1) / 10
}

// cvss3BaseScore computes the base score of a CVSS v3.x vector such as
// "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H". It returns false when the
// vector is not a complete, valid v3 vector.
func cvss3BaseScore(vector string) (float64, bool) {
	parts := strings.Split(vector, "/")
	if len(parts) < 9 || (parts[0] != "CVSS:3.0" && parts[0] != "CVSS:3.1") {
		return 0, false
	}

	m := make(map[string]string, 8)

	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, ":")
		if !ok {
			return 0, false
		}

		m[k] = v
	}

	if m["S"] != "C" && m["S"] != "U" {
		return 0, false
	}

	changed := m["S"] == "C"

	w := make(map[string]float64, 7)

	for _, metric := range []string{"AV", "AC", "UI", "C", "I", "A", "PR"} {
		v, ok := cvssWeight(metric, m[metric], changed)
		if !ok {
			return 0, false
		}

		w[metric] = v
	}

	av, ac, ui, pr := w["AV"], w["AC"], w["UI"], w["PR"]
	c, i, a := w["C"], w["I"], w["A"]

	iss := 1 - (1-c)*(1-i)*(1-a)

	var impact float64
	if changed {
		impact = 7.52*(iss-0.029) - 3.25*math.Pow(iss-0.02, 15)
	} else {
		impact = 6.42 * iss
	}

	if impact <= 0 {
		return 0, true
	}

	exploit := 8.22 * av * ac * pr * ui

	if changed {
		return cvssRoundUp(math.Min(1.08*(impact+exploit), 10)), true
	}

	return cvssRoundUp(math.Min(impact+exploit, 10)), true
}
