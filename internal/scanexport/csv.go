// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package scanexport

import (
	"encoding/csv"
	"io"
	"strconv"
)

func itoa(n int) string { return strconv.Itoa(n) }

// SafeCell neutralizes spreadsheet formula injection: a cell that starts with
// "=", "+", "-", "@", a tab or a carriage return would be evaluated by Excel or
// LibreOffice, so it is prefixed with a single quote (OWASP CSV injection).
func SafeCell(s string) string {
	if s == "" {
		return s
	}

	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}

	return s
}

// WriteCSV writes header and rows as RFC 4180 CSV with every cell guarded
// against formula injection.
func WriteCSV(w io.Writer, header []string, rows [][]string) error {
	cw := csv.NewWriter(w)

	if err := cw.Write(header); err != nil {
		return err
	}

	rec := make([]string, len(header))

	for _, row := range rows {
		for i, c := range row {
			rec[i] = SafeCell(c)
		}

		if err := cw.Write(rec); err != nil {
			return err
		}
	}

	cw.Flush()

	return cw.Error()
}
