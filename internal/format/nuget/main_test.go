// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/pkgdeps"
)

// init registers the manifest rule, the dependency and the license hooks of the format, the
// way internal/formats does at startup: the handler reads the license of an
// upload through licensemeta, which dispatches by format. The package cannot
// import internal/formats, which imports it.
// It is an init, not a TestMain, so the fuzz targets registered by ClusterFuzzLite
// (which compiles the _test.go files into the fuzzer but never runs TestMain)
// see the same registry.
func init() { //nolint:gochecknoinits // test-only format registry, see above
	manifest.Register("nuget", manifest.NuGetRule)
	licensemeta.Register("nuget", licensemeta.NuGetHooks)
	pkgdeps.Register("nuget", pkgdeps.NuGetHooks)
}
