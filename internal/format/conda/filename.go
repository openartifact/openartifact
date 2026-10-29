// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	pkgconda "github.com/openartifact/openartifact/pkg/conda"
)

// validPlatform reports whether platform, the subdirectory of a repository
// packages are published under (linux-64, noarch), fits package_files.arch.
// Any other value is a database error, not a package.
func validPlatform(platform string) bool {
	return pkgconda.ValidSubdir(platform)
}
