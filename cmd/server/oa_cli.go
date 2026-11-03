// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "path/filepath"

// cliName is the program name that selects the embedded oa CLI.
const cliName = "oa"

// cliArgs reports whether the process was started as the oa CLI, and with
// which arguments. The container image ships one executable: /usr/local/bin/oa
// is a link to it, so `oa apply` runs the CLI (argv[0] is oa), and
// `openartifact oa apply` does the same without the link.
func cliArgs(argv []string) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}

	if filepath.Base(argv[0]) == cliName {
		return argv[1:], true
	}

	if len(argv) > 1 && argv[1] == cliName {
		return argv[2:], true
	}

	return nil, false
}
