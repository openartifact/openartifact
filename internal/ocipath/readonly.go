// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ocipath

import (
	"encoding/json"
	"net/http"
)

// WriteError writes a distribution-spec error response.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	type errBody struct {
		Code    string  `json:"code"`
		Message string  `json:"message"`
		Detail  *string `json:"detail"`
	}

	body, err := json.Marshal(struct {
		Errors []errBody `json:"errors"`
	}{[]errBody{{Code: code, Message: message}}})
	if err == nil {
		_, _ = w.Write(append(body, '\n'))
	}
}

// RejectNonPull answers what a read-only registry view (a proxy or group
// repository) cannot serve: unknown endpoints, upload sessions, and any method
// other than GET/HEAD. It reports whether the response was written.
func RejectNonPull(w http.ResponseWriter, r *http.Request, kind Kind) bool {
	readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead

	switch {
	case kind == KindUnknown:
		WriteError(w, http.StatusNotFound, "NAME_UNKNOWN", "unknown registry endpoint")
	case kind == KindUpload && readOnly:
		WriteError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "this repository takes no uploads")
	case kind == KindUploadStart || !readOnly:
		w.Header().Set("Allow", "GET, HEAD")
		WriteError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "proxy and group repositories are read-only")
	default:
		return false
	}

	return true
}

// NotFoundCode is the registry error code for a missing resource of kind.
func NotFoundCode(kind Kind) (string, string) {
	switch kind {
	case KindBlob:
		return "BLOB_UNKNOWN", "blob not found"
	case KindManifest:
		return "MANIFEST_UNKNOWN", "manifest not found"
	case KindUnknown, KindUploadStart, KindUpload, KindTags, KindReferrers:
	}

	return "NAME_UNKNOWN", "repository name not known to registry"
}
