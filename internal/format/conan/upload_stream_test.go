// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

// The wire digests of a body big enough to be spooled to disk are those of the
// whole body, the SHA-256 is the key of the stored blob, and a later checksum
// deploy finds the file by its SHA-1.
func TestUploadLargeFileDigests(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	e := newEnv(t)

	e.publishRecipe(rrev1)

	body := strings.Repeat("0123456789abcdef", 3<<16) // 3 MiB
	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), body, http.StatusCreated)

	row := e.file(rrev1, "conanfile.py")
	sum := sha256.Sum256([]byte(body))

	if row.SHA256 != hex.EncodeToString(sum[:]) || row.SHA1 != sha1Hex(body) || row.MD5 != md5Hex(body) || row.SizeBytes != int64(len(body)) {
		t.Errorf("row = %+v, want the digests of the %d-byte body", row, len(body))
	}

	if got := e.mustDo(http.MethodGet, recipeFileURL(rrev1, "conanfile.py"), "", http.StatusOK).Body.String(); got != body {
		t.Errorf("downloaded %d bytes, want the upload (%d)", len(got), len(body))
	}

	if code := e.deploy(recipeFileURL(rrev2, "conanfile.py"), sha1Hex(body)); code != http.StatusCreated {
		t.Errorf("checksum deploy of the large file = %d, want 201", code)
	}
}
