// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift

import (
	"encoding/json"
	"net/http"
)

// WriteProblem answers with an RFC 7807 problem document, the error format of
// the registry protocol: {"detail": ...} as application/problem+json.
func WriteProblem(w http.ResponseWriter, status int, detail string) {
	body, err := json.Marshal(Problem{Detail: detail})
	if err != nil {
		http.Error(w, detail, status)
		return
	}

	h := w.Header()
	h.Set("Content-Version", ContentVersion)
	h.Set("Content-Type", ContentTypeProblem)
	h.Set("Content-Language", "en")
	h.Del("Content-Length")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// CheckRequest marks the response as version 1 of the registry API
// (Content-Version) and enforces the request's Accept header: a request for an
// unknown API version is answered 400 and one for a version that is not
// supported 415. It returns false after writing the answer.
func CheckRequest(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Content-Version", ContentVersion)

	switch CheckAccept(r.Header.Values("Accept")) {
	case http.StatusBadRequest:
		WriteProblem(w, http.StatusBadRequest, "invalid Accept header: unknown API version")
		return false
	case http.StatusUnsupportedMediaType:
		WriteProblem(w, http.StatusUnsupportedMediaType, "unsupported API version; this registry serves version 1")

		return false
	}

	return true
}
