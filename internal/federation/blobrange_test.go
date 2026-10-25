// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"errors"
	"testing"
)

func TestParseRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		header  string
		size    int64
		want    blobSpan
		wantErr bool
	}{
		{"absent selects the whole blob", "", 100, blobSpan{}, false},
		{"unknown size ignores the range", "bytes=10-", 0, blobSpan{}, false},
		{"open ended", "bytes=40-", 100, blobSpan{start: 40, end: 99, partial: true}, false},
		{"from zero is still partial", "bytes=0-", 100, blobSpan{start: 0, end: 99, partial: true}, false},
		{"bounded", "bytes=10-19", 100, blobSpan{start: 10, end: 19, partial: true}, false},
		{"end clamped to the last byte", "bytes=90-500", 100, blobSpan{start: 90, end: 99, partial: true}, false},
		{"last byte", "bytes=99-", 100, blobSpan{start: 99, end: 99, partial: true}, false},
		{"start at the size", "bytes=100-", 100, blobSpan{}, true},
		{"start past the size", "bytes=101-", 100, blobSpan{}, true},
		{"end before start", "bytes=20-10", 100, blobSpan{}, true},
		{"suffix range", "bytes=-5", 100, blobSpan{}, true},
		{"several ranges", "bytes=0-1,5-6", 100, blobSpan{}, true},
		{"other unit", "items=0-1", 100, blobSpan{}, true},
		{"not a number", "bytes=a-", 100, blobSpan{}, true},
		{"negative start", "bytes=-1-", 100, blobSpan{}, true},
		{"no dash", "bytes=5", 100, blobSpan{}, true},
		{"empty spec", "bytes=", 100, blobSpan{}, true},
		{"bad end", "bytes=5-x", 100, blobSpan{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseRange(tt.header, tt.size)
			if tt.wantErr {
				if !errors.Is(err, errBadRange) {
					t.Fatalf("parseRange(%q, %d) error = %v, want errBadRange", tt.header, tt.size, err)
				}

				return
			}

			if err != nil || got != tt.want {
				t.Fatalf("parseRange(%q, %d) = %+v, %v; want %+v", tt.header, tt.size, got, err, tt.want)
			}
		})
	}
}

func TestParseContentRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		header  string
		want    contentRange
		wantErr bool
	}{
		{"bytes 5-9/10", contentRange{5, 9, 10}, false},
		{"bytes 0-0/1", contentRange{0, 0, 1}, false},
		{"bytes 5-9/*", contentRange{5, 9, -1}, false},
		{"", contentRange{}, true},
		{"items 5-9/10", contentRange{}, true},
		{"bytes 5-9", contentRange{}, true},
		{"bytes 5/10", contentRange{}, true},
		{"bytes 9-5/10", contentRange{}, true},
		{"bytes a-5/10", contentRange{}, true},
		{"bytes */10", contentRange{}, true},
		{"bytes 5-9/x", contentRange{}, true},
	}

	for _, tt := range tests {
		got, err := parseContentRange(tt.header)
		if tt.wantErr {
			if !errors.Is(err, errBadContentRange) {
				t.Errorf("parseContentRange(%q) error = %v, want errBadContentRange", tt.header, err)
			}

			continue
		}

		if err != nil || got != tt.want {
			t.Errorf("parseContentRange(%q) = %+v, %v; want %+v", tt.header, got, err, tt.want)
		}
	}
}
