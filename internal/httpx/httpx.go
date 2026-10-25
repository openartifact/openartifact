// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpx holds the JSON response writers shared by the HTTP layers, so
// every one of them answers with the same envelope.
package httpx

import (
	"encoding/json"
	"net/http"
)

// ErrorBody is the JSON envelope of an error response.
type ErrorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// WriteJSON writes v as an application/json response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	// The header is already out: a failed encode is a broken peer connection.
	_ = json.NewEncoder(w).Encode(v) //nolint:errchkjson // generic helper
}

// WriteError writes the {error, code} envelope with the given status.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: message, Code: code})
}
