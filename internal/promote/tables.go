// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"context"
	"errors"
)

// tablesPromoter promotes a format whose artifacts live in several tables
// (terraform: modules and providers), one rowPromoter per table. The item's
// content is held by one table of the source repository, so the first table
// that has it promotes it.
type tablesPromoter struct {
	format string
	parts  []rowPromoter
}

func (p *tablesPromoter) Format() string { return p.format }

// Promote implements Promoter.
func (p *tablesPromoter) Promote(ctx context.Context, tx Tx, req Request) (*Result, error) {
	for _, part := range p.parts {
		res, err := part.Promote(ctx, tx, req)
		if errors.Is(err, ErrItemNotFound) {
			continue
		}

		return res, err
	}

	return nil, ErrItemNotFound
}
