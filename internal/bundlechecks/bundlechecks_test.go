// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package bundlechecks

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Hour)

	check := func(name string, status models.BundleCheckStatus, expires *time.Time) *models.BundleCheck {
		return &models.BundleCheck{Name: name, Status: status, ExpiresAt: expires}
	}

	tests := []struct {
		name     string
		required []string
		reported []*models.BundleCheck
		want     Verdict
	}{
		{"nothing required", nil, nil, Verdict{}},
		{"success", []string{"a"}, []*models.BundleCheck{check("a", models.BundleCheckSuccess, nil)}, Verdict{}},
		{"success until the future", []string{"a"}, []*models.BundleCheck{check("a", models.BundleCheckSuccess, &future)}, Verdict{}},
		{"never reported", []string{"a"}, nil, Verdict{Missing: []string{"a"}}},
		{
			"other check does not count",
			[]string{"a"},
			[]*models.BundleCheck{check("b", models.BundleCheckSuccess, nil)},
			Verdict{Missing: []string{"a"}},
		},
		{"failure", []string{"a"}, []*models.BundleCheck{check("a", models.BundleCheckFailure, nil)}, Verdict{Failed: []string{"a"}}},
		{"error", []string{"a"}, []*models.BundleCheck{check("a", models.BundleCheckError, nil)}, Verdict{Failed: []string{"a"}}},
		{"pending", []string{"a"}, []*models.BundleCheck{check("a", models.BundleCheckPending, nil)}, Verdict{Pending: []string{"a"}}},
		{
			"expired success",
			[]string{"a"},
			[]*models.BundleCheck{check("a", models.BundleCheckSuccess, &past)},
			Verdict{Expired: []string{"a"}},
		},
		{
			"expiring exactly now is expired",
			[]string{"a"},
			[]*models.BundleCheck{check("a", models.BundleCheckSuccess, &now)},
			Verdict{Expired: []string{"a"}},
		},
		{
			"failure stays failure when expired",
			[]string{"a"},
			[]*models.BundleCheck{check("a", models.BundleCheckFailure, &past)},
			Verdict{Failed: []string{"a"}},
		},
		{
			"mixed, in required order", []string{"z", "m", "a", "p", "ok"},
			[]*models.BundleCheck{
				check("a", models.BundleCheckFailure, nil), check("p", models.BundleCheckPending, nil),
				check("m", models.BundleCheckSuccess, &past), check("ok", models.BundleCheckSuccess, nil),
			},
			Verdict{Missing: []string{"z"}, Expired: []string{"m"}, Failed: []string{"a"}, Pending: []string{"p"}},
		},
		{"duplicate required names count once", []string{"a", "a"}, nil, Verdict{Missing: []string{"a"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(tt.required, tt.reported, now)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Evaluate = %+v, want %+v", got, tt.want)
			}

			if got.Satisfied() != reflect.DeepEqual(tt.want, Verdict{}) {
				t.Errorf("Satisfied = %v for %+v", got.Satisfied(), got)
			}
		})
	}
}

func TestVerdictString(t *testing.T) {
	v := Verdict{Missing: []string{"a", "b"}, Failed: []string{"c"}, Pending: []string{"d"}, Expired: []string{"e"}}

	if got, want := v.String(), "missing: a, b; failed: c; pending: d; expired: e"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}

	if got := (Verdict{}).String(); got != "" {
		t.Errorf("a satisfied verdict has no message, got %q", got)
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name, in string
		valid    bool
	}{
		{"plain", "conforma", true},
		{"punctuation and spaces inside", "ec / policy: release (prod)", true},
		{"empty", "", false},
		{"leading space", " a", false},
		{"trailing newline", "a\n", false},
		{"control character", "a\x00b", false},
		{"too long", strings.Repeat("a", MaxNameLen+1), false},
		{"longest", strings.Repeat("a", MaxNameLen), true},
	}

	for _, tt := range tests {
		if got := ValidateName(tt.in) == ""; got != tt.valid {
			t.Errorf("%s: ValidateName(%q) valid = %v, want %v", tt.name, tt.in, got, tt.valid)
		}
	}
}

func TestValidateRequired(t *testing.T) {
	many := make([]string, MaxPerStage+1)
	for i := range many {
		many[i] = "c" + strings.Repeat("x", i)
	}

	tests := []struct {
		name  string
		in    []string
		valid bool
	}{
		{"none", nil, true},
		{"two", []string{"a", "b"}, true},
		{"duplicate", []string{"a", "a"}, false},
		{"bad name", []string{"a", ""}, false},
		{"at the limit", many[:MaxPerStage], true},
		{"over the limit", many, false},
	}

	for _, tt := range tests {
		if got := ValidateRequired(tt.in) == ""; got != tt.valid {
			t.Errorf("%s: valid = %v, want %v", tt.name, got, tt.valid)
		}
	}
}

func TestValidStatus(t *testing.T) {
	for _, s := range []models.BundleCheckStatus{models.BundleCheckPending, models.BundleCheckSuccess, models.BundleCheckFailure, models.BundleCheckError} {
		if !ValidStatus(s) {
			t.Errorf("%q must be valid", s)
		}
	}

	for _, s := range []models.BundleCheckStatus{"", "passed", "SUCCESS"} {
		if ValidStatus(s) {
			t.Errorf("%q must be invalid", s)
		}
	}
}
