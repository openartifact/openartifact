// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"os"
	"testing"

	"github.com/openartifact/openartifact/internal/formats"
)

//nolint:gochecknoglobals // TestMain-shared harness, initialised once for the whole package
var harness *TestHarness

func TestMain(m *testing.M) {
	formats.Register()

	ctx := context.Background()

	h, err := NewHarness(ctx)
	if err != nil {
		panic("failed to create test harness: " + err.Error())
	}
	harness = h

	code := m.Run()

	harness.Close(ctx)
	os.Exit(code)
}
