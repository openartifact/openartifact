// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/scanexport"
)

const (
	exportFormatSARIF = "sarif"
	exportFormatCSV   = "csv"
	exportFormatJSON  = "json"

	// exportTruncatedHeader is "true" when an export holds back findings or
	// scans because of a size limit.
	exportTruncatedHeader = "X-Export-Truncated"
)

// exportFormat reads ?format (default json). ok is false for an unknown value.
func exportFormat(r *http.Request) (string, bool) {
	switch f := strings.ToLower(r.URL.Query().Get("format")); f {
	case "":
		return exportFormatJSON, true
	case exportFormatSARIF, exportFormatCSV, exportFormatJSON:
		return f, true
	}

	return "", false
}

// exportFileName builds a download name from parts, keeping only characters
// that are safe in a Content-Disposition filename.
func exportFileName(ext string, parts ...string) string {
	var b strings.Builder

	for i, p := range parts {
		if i > 0 {
			b.WriteByte('-')
		}

		for _, c := range p {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
				b.WriteRune(c)
			default:
				b.WriteByte('_')
			}
		}
	}

	return b.String() + "." + ext
}

// exportHeaders sets the download headers for format.
func exportHeaders(w http.ResponseWriter, format, filename string, truncated bool) {
	switch format {
	case exportFormatSARIF:
		w.Header().Set("Content-Type", "application/sarif+json")
	case exportFormatCSV:
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/json")
	}

	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if truncated {
		w.Header().Set(exportTruncatedHeader, "true")
	}
}

// writeExportJSON writes v as the body of a JSON-encoded download.
func writeExportJSON(w http.ResponseWriter, v any) {
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	if encErr := enc.Encode(v); encErr != nil {
		_ = encErr // response header already written
	}
}

// writeExportCSV writes the CSV body.
func writeExportCSV(w http.ResponseWriter, header []string, rows [][]string) {
	w.WriteHeader(http.StatusOK)

	if err := scanexport.WriteCSV(w, header, rows); err != nil {
		_ = err // response header already written
	}
}
