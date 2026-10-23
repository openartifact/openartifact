// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

// Promoters returns the promoter of every hosted format a bundle can hold.
func Promoters() []Promoter {
	ps := []Promoter{
		npmPromoter{},
		gomodPromoter{},
		conanPromoter{},
		dockerPromoter{},
		debPromoter{},
		rpmPromoter{},
	}

	for _, spec := range rowSpecs() {
		ps = append(ps, rowPromoter{spec: spec})
	}

	return ps
}
