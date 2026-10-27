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

	byFormat := map[string]*tablesPromoter{}

	for _, spec := range rowSpecs() {
		tp, ok := byFormat[spec.format]
		if !ok {
			tp = &tablesPromoter{format: spec.format}
			byFormat[spec.format] = tp
			ps = append(ps, tp)
		}

		tp.parts = append(tp.parts, rowPromoter{spec: spec})
	}

	return ps
}
