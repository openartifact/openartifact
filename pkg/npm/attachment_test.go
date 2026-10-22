// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"errors"
	"testing"
)

func TestCheckAttachmentContentType(t *testing.T) {
	if err := CheckAttachmentContentType("application/octet-stream", true); err != nil {
		t.Fatalf("valid content type rejected: %v", err)
	}

	for name, tc := range map[string]struct {
		ct string
		ok bool
	}{"wrong": {"text/plain", true}, "missing": {"", false}} {
		if err := CheckAttachmentContentType(tc.ct, tc.ok); !errors.Is(err, ErrInvalidAttachment) {
			t.Errorf("%s: expected ErrInvalidAttachment, got %v", name, err)
		}
	}
}

func TestCheckAttachmentLength(t *testing.T) {
	if err := CheckAttachmentLength(5, true, 5); err != nil {
		t.Fatalf("matching length rejected: %v", err)
	}

	for name, tc := range map[string]struct {
		declared int64
		ok       bool
		actual   int64
	}{"mismatch": {99, true, 5}, "missing": {0, false, 0}} {
		if err := CheckAttachmentLength(tc.declared, tc.ok, tc.actual); !errors.Is(err, ErrInvalidAttachment) {
			t.Errorf("%s: expected ErrInvalidAttachment, got %v", name, err)
		}
	}
}
