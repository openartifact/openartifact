// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCappedReaderStopsAtLimit(t *testing.T) {
	_, err := io.ReadAll(&cappedReader{r: strings.NewReader(strings.Repeat("x", 100)), n: 10})
	if !errors.Is(err, ErrChartTooLarge) {
		t.Fatalf("err = %v, want ErrChartTooLarge", err)
	}
}
