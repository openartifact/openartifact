// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quayapi

import "net/http"

// errorBody is the Quay error envelope. The image-controller reads error_message
// for most endpoints and message for the robot and permission endpoints, so
// both carry the text.
type errorBody struct {
	ErrorMessage string `json:"error_message"`
	Message      string `json:"message"`
	Status       int    `json:"status"`
}

// writeError answers with a Quay-shaped error.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody{ErrorMessage: message, Message: message, Status: status})
}
