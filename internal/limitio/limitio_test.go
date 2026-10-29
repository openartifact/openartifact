// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package limitio

import (
	"errors"
	"io"
	"strings"
	"testing"
)

var errBig = errors.New("too big")

func TestCappedStopsAtLimit(t *testing.T) {
	got, err := io.ReadAll(Capped(strings.NewReader(strings.Repeat("x", 100)), 10, errBig))
	if !errors.Is(err, errBig) {
		t.Fatalf("err = %v, want errBig", err)
	}

	if len(got) != 10 {
		t.Fatalf("read %d bytes before failing, want 10", len(got))
	}
}

func TestReadAllLimit(t *testing.T) {
	got, err := ReadAll(strings.NewReader("12345"), 5, errBig)
	if err != nil || string(got) != "12345" {
		t.Fatalf("ReadAll at the limit = %q, %v; want the input", got, err)
	}

	if _, err := ReadAll(strings.NewReader("123456"), 5, errBig); !errors.Is(err, errBig) {
		t.Fatalf("ReadAll over the limit: err = %v, want errBig", err)
	}
}
