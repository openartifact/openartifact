// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/pkg/gitlfs"
)

// gitLFSBatchPath is the artifact path of the Git LFS batch endpoint, the one
// POST of the protocol whose permission depends on its body.
const gitLFSBatchPath = "objects/batch"

// isGitLFSBatch reports whether r is POST /repos/gitlfs/{name}/objects/batch.
func isGitLFSBatch(r *http.Request, artifactPath string) bool {
	return r.Method == http.MethodPost && artifactPath == gitLFSBatchPath && strings.HasPrefix(r.URL.Path, "/repos/gitlfs/")
}

// gitLFSBatchAction is the permission a batch request needs. A batch is a POST
// but only asks where objects can be fetched when its operation is "download",
// which a reader may do (git lfs pull on a public or read-only repository);
// every other operation, and every body that cannot be read as a batch within
// the size the handler accepts, needs "write". The body is read through
// gitlfs.PeekOperation, which decodes the operation the way the handler's
// gitlfs.ParseBatch does, and restored for the handler.
func gitLFSBatchAction(r *http.Request) string {
	if r.Body == nil || r.Body == http.NoBody {
		return "write"
	}

	buf, err := io.ReadAll(io.LimitReader(r.Body, gitlfs.MaxBatchBytes+1))

	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}

	if err != nil || len(buf) > gitlfs.MaxBatchBytes {
		return "write"
	}

	if gitlfs.PeekOperation(buf) == gitlfs.OperationDownload {
		return "read"
	}

	return "write"
}
