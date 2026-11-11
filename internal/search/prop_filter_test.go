// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckPropFilters: the rules GET /api/v1/search and the MCP tool share, with the
// limits of the property API.
func TestCheckPropFilters(t *testing.T) {
	var ten []string
	for range maxPropFilters {
		ten = append(ten, "k=v")
	}

	for name, tc := range map[string]struct {
		props []string
		want  error
	}{
		"none":                    {nil, nil},
		"key and value":           {[]string{"status=approved"}, nil},
		"empty value":             {[]string{"status="}, nil},
		"value with = and spaces": {[]string{"note=a=b c"}, nil},
		"ten filters":             {ten, nil},
		"eleven filters":          {append(ten, "k=v"), ErrTooManyPropFilters},
		"no =":                    {[]string{"status"}, ErrInvalidPropFilter},
		"empty key":               {[]string{"=approved"}, ErrInvalidPropFilter},
		"key too long":            {[]string{strings.Repeat("k", maxPropKeyBytes+1) + "=v"}, ErrInvalidPropFilter},
		"value too long":          {[]string{"k=" + strings.Repeat("v", maxPropValueBytes+1)}, ErrInvalidPropFilter},
		"control character":       {[]string{"k\x01=v"}, ErrInvalidPropFilter},
		"NUL in value":            {[]string{"k=v\x00"}, ErrInvalidPropFilter},
		"invalid UTF-8":           {[]string{"k=\xff"}, ErrInvalidPropFilter},
	} {
		if err := CheckPropFilters(SearchFilter{Props: tc.props}); !errors.Is(err, tc.want) {
			t.Errorf("%s: CheckPropFilters = %v, want %v", name, err, tc.want)
		}
	}
}

// TestSplitProp splits at the first '=' only.
func TestSplitProp(t *testing.T) {
	if k, v, ok := splitProp("note=a=b"); !ok || k != "note" || v != "a=b" {
		t.Errorf("splitProp = %q %q %v", k, v, ok)
	}
}
