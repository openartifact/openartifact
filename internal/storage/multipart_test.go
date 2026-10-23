// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"errors"
	"testing"
)

func TestPlanParts(t *testing.T) {
	const gib = int64(1) << 30

	tests := []struct {
		name      string
		size      int64
		requested int64
		wantPart  int64
		wantCount int
		wantErr   bool
	}{
		{"default part size for 6 GiB", 6 * gib, 0, DefaultPartSize, 96, false},
		{"explicit part size", 10 * MinPartSize, MinPartSize, MinPartSize, 10, false},
		{"tail part", 2*MinPartSize + 1, MinPartSize, MinPartSize, 3, false},
		{"default grows to stay within 10000 parts", 5 << 40, 0, 525 << 20, 9987, false},
		{"explicit part size below minimum", gib, MinPartSize - 1, 0, 0, true},
		{"explicit part size above maximum", gib, MaxPartSize + 1, 0, 0, true},
		{"explicit part size needing too many parts", 1 << 40, 64 << 20, 0, 0, true},
		{"size above the object limit", MaxMultipartObjectBytes + 1, 0, 0, 0, true},
		{"negative size", -1, 0, 0, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			part, count, err := PlanParts(tc.size, tc.requested)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidPartPlan) {
					t.Fatalf("err = %v, want ErrInvalidPartPlan", err)
				}

				return
			}

			if err != nil || part != tc.wantPart || count != tc.wantCount {
				t.Fatalf("PlanParts = (%d, %d, %v), want (%d, %d, nil)", part, count, err, tc.wantPart, tc.wantCount)
			}
		})
	}
}

func TestPartLengthsCoverTheObject(t *testing.T) {
	for _, size := range []int64{1, MinPartSize, 3*MinPartSize + 7, 10 * MinPartSize} {
		var sum int64

		count := PartCount(size, MinPartSize)
		for n := 1; n <= count; n++ {
			l := PartLength(size, MinPartSize, n)
			if l <= 0 || l > MinPartSize {
				t.Fatalf("size %d part %d length %d", size, n, l)
			}

			sum += l
		}

		if sum != size {
			t.Errorf("size %d: parts sum to %d", size, sum)
		}

		if PartLength(size, MinPartSize, 0) != 0 || PartLength(size, MinPartSize, count+1) != 0 {
			t.Errorf("size %d: parts outside the plan must have length 0", size)
		}
	}
}
