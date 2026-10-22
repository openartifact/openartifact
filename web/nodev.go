// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !dev

package web

import "net/http"

// devHandler is a no-op in release builds.
func devHandler() http.Handler { return nil }
