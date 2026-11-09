// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"strings"
	"testing"
)

// TestCheckProjectFilters: the rules GET /api/v1/search and the MCP tool share.
func TestCheckProjectFilters(t *testing.T) {
	many := func(v string, n int) []string {
		return strings.Split(strings.TrimSuffix(strings.Repeat(v+",", n), ","), ",")
	}

	for name, tc := range map[string]struct {
		filter SearchFilter
		ok     bool
	}{
		"none":                {SearchFilter{}, true},
		"every role":          {SearchFilter{ProjectRoles: []string{"viewer", "developer", "maintainer", "owner"}}, true},
		"unknown role":        {SearchFilter{ProjectRoles: []string{"admin"}}, false},
		"roles are lowercase": {SearchFilter{ProjectRoles: []string{"Viewer"}}, false},
		"50 projects":         {SearchFilter{Projects: many("p", maxFilterValues)}, true},
		"51 projects":         {SearchFilter{Projects: many("p", maxFilterValues+1)}, false},
		"51 project_role":     {SearchFilter{ProjectRoles: many("viewer", maxFilterValues+1)}, false},
	} {
		if err := CheckProjectFilters(tc.filter); (err == nil) != tc.ok {
			t.Errorf("%s: CheckProjectFilters = %v, want ok=%v", name, err, tc.ok)
		}
	}
}
