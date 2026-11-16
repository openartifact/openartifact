// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package lockfile

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"golang.org/x/mod/module"
)

// goSumMaxLine bounds one line of a go.sum.
const goSumMaxLine = 64 << 10

// parseGoSum reads the module versions of a go.sum: "path version hash"
// lines, a version suffixed "/go.mod" when only the module's go.mod is
// checksummed. Both count: the go command fetches that file through the proxy
// as well, and the proxy gates it like the zip. go.sum lists no local or VCS
// module (a replace to a directory has no line), records no requirers
// (GraphRecorded) and no test-only modules: no Dep of it is Dev or has a
// requirer. A line the go command would refuse (not three fields, an invalid
// module path or version) makes the file invalid.
func parseGoSum(data []byte) ([]Dep, error) {
	var deps []Dep

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, goSumMaxLine)

	for n := 1; sc.Scan(); n++ {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}

		if len(fields) != 3 {
			return nil, invalid("go.sum line " + strconv.Itoa(n) + " is not \"module version hash\"")
		}

		path, version := fields[0], strings.TrimSuffix(fields[1], "/go.mod")
		if err := module.Check(path, version); err != nil {
			return nil, invalid("go.sum line " + strconv.Itoa(n) + ": " + err.Error())
		}

		deps = append(deps, Dep{Name: path, Version: version})
	}

	if err := sc.Err(); err != nil {
		return nil, invalid(err.Error())
	}

	return deps, nil
}
