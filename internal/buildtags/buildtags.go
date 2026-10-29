// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package buildtags reports which optional features this binary was compiled
// with: AI analysis (left out by -tags noai) and the MCP server (left out by
// -tags nomcp). The constants are compile-time, so code behind them is
// removed where the compiler can prove it dead; the feature packages
// themselves are kept out by build constraints (docs/development/README.md#build-tags).
package buildtags
