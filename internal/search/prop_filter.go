// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxPropFilters caps the property filters of one search.
	maxPropFilters = 10
	// The limits of the property API (internal/api/properties.go).
	maxPropKeyBytes   = 255
	maxPropValueBytes = 4096
)

// Errors of CheckPropFilters.
var (
	ErrInvalidPropFilter = errors.New(
		"prop must be KEY=VALUE: a key of 1 to 255 bytes without control characters, a value of at most 4096 bytes",
	)
	ErrTooManyPropFilters = fmt.Errorf("at most %d prop filters", maxPropFilters)
)

// splitProp splits a KEY=VALUE property filter at its first '='.
func splitProp(raw string) (string, string, bool) {
	key, value, ok := strings.Cut(raw, "=")

	return key, value, ok && key != ""
}

// CheckPropFilters applies the rules GET /api/v1/search answers with
// 400 invalid_filter to the property filters, and that the MCP search tool shares:
// at most maxPropFilters, each KEY=VALUE within the limits the property API stores
// (valid UTF-8; no control characters in the key, no NUL in the value).
func CheckPropFilters(f SearchFilter) error {
	if len(f.Props) > maxPropFilters {
		return ErrTooManyPropFilters
	}

	for _, raw := range f.Props {
		key, value, ok := splitProp(raw)
		if !ok || len(key) > maxPropKeyBytes || len(value) > maxPropValueBytes ||
			!utf8.ValidString(key) || !utf8.ValidString(value) ||
			strings.ContainsFunc(key, unicode.IsControl) || strings.ContainsRune(value, 0) {
			return ErrInvalidPropFilter
		}
	}

	return nil
}

// propByIDLimit is the most properties a filter may match to be applied by id
// lookups (whereBuilder.addPropByID); a broader one is applied as a correlated EXISTS
// (whereBuilder.addPropExists). Measured on 300k tagged files: 25 matches take about
// 1 ms by id, while the EXISTS reads every file; all 300k take 1.6 s by id (one probe
// each) against 49 ms for the EXISTS.
const propByIDLimit = 1000

// planProps decides, per property filter, how the search applies it, from how many
// properties match (counted up to propByIDLimit+1 with idx_artifact_properties_key_value).
// Both forms select the same rows; only the plan differs. A filter whose shapes are
// already set is left as it is.
func (e *Executor) planProps(ctx context.Context, filter SearchFilter) (SearchFilter, error) {
	if len(filter.Props) == 0 || len(filter.propByID) == len(filter.Props) {
		return filter, nil
	}

	byID := make([]bool, len(filter.Props))

	for i, raw := range filter.Props {
		key, value, _ := splitProp(raw)

		var n int
		if err := e.db.QueryRowContext(ctx,
			`SELECT count(*) FROM (SELECT 1 FROM artifact_properties WHERE key = $1 AND value = $2 LIMIT $3) c`,
			key, value, propByIDLimit+1).Scan(&n); err != nil {
			return filter, fmt.Errorf("plan property filter: %w", err)
		}

		byID[i] = n <= propByIDLimit
	}

	filter.propByID = byID

	return filter, nil
}
