// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// FuzzOCIPaginateTags pages a group's merged, sorted tag list with arbitrary
// n and last query values: a page is the run of tags after last that n allows,
// the resume tag exists exactly when more follow, and following the resume
// tags walks every tag after last exactly once.
func FuzzOCIPaginateTags(f *testing.F) {
	f.Add("a\nb\nc\nd\ne", "", "2")
	f.Add("a\nb\nc", "b", "")
	f.Add("a\nb\nc", "zz", "5")
	f.Add("a", "a", "0")
	f.Add("1\n2", "", "-1")
	f.Add("1\n2", "", "x")
	f.Add("", "", "1")

	f.Fuzz(func(t *testing.T, blob, last, nStr string) {
		// The merged tag list is sorted and unique, which paginateTags relies on.
		tags := slices.DeleteFunc(strings.Split(blob, "\n"), func(s string) bool { return s == "" })
		sort.Strings(tags)
		tags = slices.Compact(tags)

		rest := slices.DeleteFunc(slices.Clone(tags), func(tag string) bool { return last != "" && tag <= last })

		page, next, ok := paginateTags(tags, last, nStr)

		n, convErr := strconv.Atoi(nStr)
		if nStr != "" && (convErr != nil || n < 0) {
			if ok {
				t.Fatalf("paginateTags accepted n=%q", nStr)
			}

			return
		}

		if !ok || page == nil || len(page) > len(rest) || !slices.Equal(page, rest[:len(page)]) {
			t.Fatalf("paginateTags(last=%q, n=%q) = %v, %q, %v; the tags after last are %v", last, nStr, page, next, ok, rest)
		}

		checkResumeTag(t, page, next, rest, n, nStr != "")

		if n >= 1 && nStr != "" {
			checkWalk(t, tags, rest, last, n)
		}
	})
}

// checkResumeTag: a page is at most n long and names its last tag as the
// resume tag exactly when more tags follow it.
func checkResumeTag(t *testing.T, page []string, next string, rest []string, n int, limited bool) {
	t.Helper()

	more := limited && n > 0 && n < len(rest)

	switch {
	case more && (len(page) != n || next != page[n-1]):
		t.Fatalf("page %v of %v (n=%d) has resume tag %q", page, rest, n, next)
	case !more && next != "":
		t.Fatalf("last page %v of %v has resume tag %q", page, rest, next)
	case limited && len(page) > n:
		t.Fatalf("page %v is longer than n=%d", page, n)
	}
}

// checkWalk follows the resume tags: every tag after last is listed exactly
// once, in order, and the walk ends.
func checkWalk(t *testing.T, tags, rest []string, last string, n int) {
	t.Helper()

	var walked []string

	for cursor, pages := last, 0; ; pages++ {
		if pages > len(tags)+1 {
			t.Fatalf("paging %v with n=%d from %q does not end", tags, n, last)
		}

		page, next, _ := paginateTags(tags, cursor, strconv.Itoa(n))
		walked = append(walked, page...)

		if next == "" {
			break
		}

		cursor = next
	}

	if !slices.Equal(walked, rest) {
		t.Fatalf("paging %v with n=%d from %q listed %v, want %v", tags, n, last, walked, rest)
	}
}

// FuzzOCIMemberTags reads the tag listing of a member whose pages (status, Link
// header and body) are arbitrary, as a proxied upstream registry's are: the
// read follows at most maxMemberTagPages pages, reports a listing it could not
// read to the end as failed, returns nothing for a member that does not know
// the image, and only ever returns tags a served page carried.
func FuzzOCIMemberTags(f *testing.F) {
	f.Add("{\"tags\":[\"a\",\"b\"]}\x00{\"tags\":[\"c\"]}", "</v2/x/tags/list?last=b&n=2>; rel=\"next\"\x00", []byte{0, 0})
	f.Add("{\"tags\":[\"a\"]}", "</v2/x/tags/list?last=a>; rel=\"next\"", []byte{0})
	f.Add("{\"tags\":[\"a\"]}\x00oops", "</v2/x/tags/list?last=a>; rel=\"next\"\x00", []byte{0, 0})
	f.Add("{\"tags\":null}", "", []byte{0})
	f.Add("", "", []byte{4})
	f.Add("{\"tags\":[\"a\"]}", "<%zz>; rel=\"next\"", []byte{0})
	f.Add("{\"tags\":[\"a\"]}\x00{\"tags\":[\"b\"]}", "</x?last=a>; rel=next\x00", []byte{0, 5})

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		f.Fatal(err)
	}

	h := &Handler{log: log}

	f.Fuzz(func(t *testing.T, bodies, links string, statuses []byte) {
		pageBodies, pageLinks := strings.Split(bodies, "\x00"), strings.Split(links, "\x00")
		served := 0

		be := Backend{ServeMember: func(w http.ResponseWriter, _ *http.Request, _ *models.Repository, _ string) {
			i := served
			served++

			status := http.StatusOK
			if i < len(statuses) {
				status = []int{200, 200, 206, 301, 404, 500, 503}[int(statuses[i])%7]
			}

			if i < len(pageLinks) {
				w.Header().Set("Link", pageLinks[i])
			}

			w.WriteHeader(status)

			if i < len(pageBodies) {
				_, _ = w.Write([]byte(pageBodies[i]))
			}
		}}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v2/x/tags/list", http.NoBody)

		tags, found, failed := h.memberTags(req, &models.Repository{Name: "m"}, "x/tags/list", be)

		if served == 0 || served > maxMemberTagPages {
			t.Fatalf("memberTags read %d pages", served)
		}

		if !found && len(tags) != 0 {
			t.Fatalf("member does not know the image but memberTags returned %v, failed = %v", tags, failed)
		}

		carried := map[string]struct{}{}

		for _, body := range pageBodies[:min(served, len(pageBodies))] {
			var doc struct {
				Tags []string `json:"tags"`
			}

			_ = json.Unmarshal([]byte(body), &doc)

			for _, tag := range doc.Tags {
				carried[tag] = struct{}{}
			}
		}

		for _, tag := range tags {
			if _, ok := carried[tag]; !ok {
				t.Fatalf("memberTags returned %q, which no served page carries", tag)
			}
		}
	})
}

// FuzzOCINextPageQuery feeds arbitrary Link headers of a member's tag listing:
// only the n and last parameters are ever carried over to the next request,
// and a header that names no next page reports no more pages.
func FuzzOCINextPageQuery(f *testing.F) {
	f.Add(`</v2/library/alpine/tags/list?last=3.9&n=100>; rel="next"`)
	f.Add(`<https://evil.example/x?n=1&last=a&evil=1>; rel=next`)
	f.Add(`<%zz>; rel="next"`)
	f.Add(`</v2/x/tags/list?n=1>; rel="prev"`)
	f.Add(`<?n=1&n=2&last=>;rel="next"`)
	f.Add("")

	f.Fuzz(func(t *testing.T, link string) {
		q, more := nextPageQuery(link)

		if !more && q != nil {
			t.Fatalf("nextPageQuery(%q) has no next page but query %v", link, q)
		}

		for k, v := range q {
			if (k != "n" && k != "last") || len(v) != 1 || v[0] == "" {
				t.Fatalf("nextPageQuery(%q) carries %q=%v", link, k, v)
			}
		}
	})
}
