// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

// Supported reports whether licenses can be read from a file of this format.
func Supported(format, filename string) bool {
	h, ok := hooks.Lookup(format)

	return ok && h.supported(format, filename)
}
