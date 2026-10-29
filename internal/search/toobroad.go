// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/httpx"
)

// TooBroadBody is the 400 answer of a query that matches more than
// MaxSearchResults packages. It is the {error, code} envelope of httpx plus what
// the caller can do about it: the cap, and the filters the query does not use
// yet, each a way to narrow the match set.
type TooBroadBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	// MaxResults is the largest match count a search answers.
	MaxResults int `json:"max_results"`
	// NarrowBy lists the filters not yet in the query, as the q: tokens or
	// parameters that apply them.
	NarrowBy []string `json:"narrow_by"`
}

// narrowingFilters returns the filters of the search the query has not applied.
func narrowingFilters(filter SearchFilter) []string {
	out := []string{}

	if filter.Name == "" {
		out = append(out, "name:<package>")
	}

	if len(filter.Formats) == 0 {
		out = append(out, "format:<format>")
	}

	if len(filter.Repos) == 0 {
		out = append(out, "repo:<repository>")
	}

	if !filter.Flagged {
		out = append(out, "flagged=true")
	}

	return out
}

// writeTooBroad answers a search that matches more than MaxSearchResults.
func writeTooBroad(w http.ResponseWriter, filter SearchFilter) {
	narrow := narrowingFilters(filter)

	msg := "query matches more than " + strconv.Itoa(MaxSearchResults) + " packages"
	if len(narrow) > 0 {
		msg += "; narrow it with " + strings.Join(narrow, ", ")
	} else {
		msg += "; use a longer or more specific search term"
	}

	httpx.WriteJSON(w, http.StatusBadRequest, TooBroadBody{
		Error:      msg,
		Code:       "query_too_broad",
		MaxResults: MaxSearchResults,
		NarrowBy:   narrow,
	})
}
