// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

// PoolPath renders the pool location of a package file inside a repository:
// pool/<component>/<prefix>/<package>/<filename>. The prefix is the first
// letter of the package name, or "lib" plus the fourth letter for lib* names
// (the layout apt expects). It is "" for an empty package name.
func PoolPath(component, packageName, filename string) string {
	if packageName == "" {
		return ""
	}

	prefix := string(packageName[0])
	if len(packageName) > 3 && packageName[:3] == "lib" {
		prefix = "lib" + string(packageName[3])
	}

	return "pool/" + component + "/" + prefix + "/" + packageName + "/" + filename
}
