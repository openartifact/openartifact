// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gitlfs is the grammar of the Git LFS server protocol
// (https://github.com/git-lfs/git-lfs/blob/main/docs/api): object ids, the batch
// request and response documents and the error bodies. It holds no policy; the
// handler (internal/format/gitlfs) and the RBAC middleware, which must agree on
// what a batch request asks for, both read requests through it.
package gitlfs

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MediaType is the Content-Type and Accept value of every JSON document of the
// protocol.
const MediaType = "application/vnd.git-lfs+json"

// Operations of a batch request.
const (
	OperationDownload = "download"
	OperationUpload   = "upload"
)

// Actions of a batch response object.
const (
	ActionDownload = "download"
	ActionUpload   = "upload"
	ActionVerify   = "verify"
)

// TransferBasic is the only transfer adapter the server speaks: one HTTP PUT or
// GET of the whole object.
const TransferBasic = "basic"

// HashSHA256 is the only hash algorithm of an object id.
const HashSHA256 = "sha256"

// MaxBatchBytes bounds a batch request body. Clients send 100 objects per batch
// by default (about 11 KB); the cap leaves room for ten thousand.
const MaxBatchBytes = 1 << 20

// oidLen is the length of a hex encoded SHA-256.
const oidLen = 64

// ErrInvalidBatch is returned for a batch request that is not a batch document.
var ErrInvalidBatch = errors.New("invalid batch request")

// ValidOID reports whether s is an object id: a lower-case hex SHA-256.
func ValidOID(s string) bool {
	if len(s) != oidLen {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// ObjectPath is the repository-wide path an object is stored under:
// objects/ab/cd/<oid>, the layout of every LFS server's store.
func ObjectPath(oid string) string {
	return "objects/" + oid[:2] + "/" + oid[2:4] + "/" + oid
}

// Pointer names one object of a batch.
type Pointer struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// Ref is the Git ref the batch is for; the server does not use it.
type Ref struct {
	Name string `json:"name"`
}

// BatchRequest is the body of POST objects/batch.
type BatchRequest struct {
	Operation string    `json:"operation"`
	Transfers []string  `json:"transfers,omitempty"`
	Ref       *Ref      `json:"ref,omitempty"`
	Objects   []Pointer `json:"objects"`
	HashAlgo  string    `json:"hash_algo,omitempty"`
}

// ParseBatch decodes a batch request. It checks the document, not its meaning:
// object ids and sizes are validated per object by the handler, so one bad
// pointer does not fail the others.
func ParseBatch(body []byte) (*BatchRequest, error) {
	var req BatchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidBatch, err)
	}

	return &req, nil
}

// PeekOperation returns the operation of a batch request body, or "" when the
// body is not a batch document. It decodes the one key ParseBatch decodes into
// BatchRequest.Operation with the same rules (case-insensitive key match, the
// last duplicate wins), so authorization and the handler never disagree on
// what a request asks for.
func PeekOperation(body []byte) string {
	var peek struct {
		Operation string `json:"operation"`
	}

	if json.Unmarshal(body, &peek) != nil {
		return ""
	}

	return peek.Operation
}

// Action is one thing the client may do with an object.
type Action struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
}

// ObjectError is a per-object failure inside a batch response.
type ObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ObjectResult is one object of a batch response: the actions the client takes
// (none when there is nothing to do) or the error that applies to it.
type ObjectResult struct {
	OID           string            `json:"oid"`
	Size          int64             `json:"size"`
	Authenticated bool              `json:"authenticated,omitempty"`
	Actions       map[string]Action `json:"actions,omitempty"`
	Error         *ObjectError      `json:"error,omitempty"`
}

// BatchResponse is the body of a successful batch.
type BatchResponse struct {
	Transfer string         `json:"transfer"`
	Objects  []ObjectResult `json:"objects"`
	HashAlgo string         `json:"hash_algo,omitempty"`
}

// Error is the body of a failed request.
type Error struct {
	Message          string `json:"message"`
	DocumentationURL string `json:"documentation_url,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
}
