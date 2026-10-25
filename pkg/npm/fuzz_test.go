// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"testing"
)

// FuzzValidatePackageName feeds arbitrary package names and versions.
func FuzzValidatePackageName(f *testing.F) {
	f.Add("@scope/name", "1.2.3-beta.1+build")
	f.Add("../../etc", "1.x")
	f.Add("", "")

	f.Fuzz(func(_ *testing.T, name, version string) {
		_ = ValidatePackageName(name)
		_ = ValidateSemver(version)
	})
}
