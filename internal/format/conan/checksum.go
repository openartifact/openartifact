// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"context"
	"crypto/md5"  //nolint:gosec // G501: Conan 1 identifies files by MD5 on the wire; not used for security
	"crypto/sha1" //nolint:gosec // G505: Conan checksum deploy is keyed by SHA-1 on the wire; not used for security
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
)

// errChecksumMiss means a checksum deploy found no file to reuse, or lost it
// to a concurrent delete: the client falls back to uploading the bytes.
var errChecksumMiss = errors.New("no file with this checksum")

// checksumDeployHeader and checksumSHA1Header are the Artifactory-style
// headers the conan client sends (conan 1 and 2): an empty PUT carrying both
// asks the server to store the file under this name if it already holds a file
// with that SHA-1.
const (
	checksumDeployHeader = "X-Checksum-Deploy"
	checksumSHA1Header   = "X-Checksum-Sha1"
)

// isChecksumDeploy reports whether the upload asks for a checksum deploy.
func isChecksumDeploy(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get(checksumDeployHeader), "true")
}

// wireDigests returns the lowercase hex MD5 and SHA-1 of data.
func wireDigests(data []byte) (string, string) {
	m := md5.Sum(data)  //nolint:gosec // see the import
	s := sha1.Sum(data) //nolint:gosec // see the import

	return hex.EncodeToString(m[:]), hex.EncodeToString(s[:])
}

// validSHA1 reports whether s is a lowercase or uppercase hex SHA-1.
func validSHA1(s string) bool {
	if len(s) != sha1.Size*2 {
		return false
	}

	_, err := hex.DecodeString(s)

	return err == nil
}

// deployByChecksum stores the scope's file with the content of a file of the
// same repository whose SHA-1 the client names: 201 when the file now exists,
// 404 when the repository holds no such content (the conan client then uploads
// it). The lookup is limited to the repository, a quarantined source is never
// reused, and the new file goes through the same quota and immutability checks
// as an upload.
func (h *HTTPHandler) deployByChecksum(w http.ResponseWriter, r *http.Request, s *scope) {
	sum := strings.ToLower(r.Header.Get(checksumSHA1Header))
	if !validSHA1(sum) {
		http.Error(w, "invalid "+checksumSHA1Header, http.StatusBadRequest)
		return
	}

	source, err := h.reusableSource(r, s.repoID, sum)
	if err != nil {
		h.fail(w, "failed to look up conan file by checksum", err, "path", s.resource()+"/"+s.filename)
		return
	}

	if source == nil {
		http.NotFound(w, r)
		return
	}

	file := s.newFile()
	file.BlobKey, file.SHA256, file.SizeBytes = source.BlobKey, source.SHA256, source.SizeBytes
	file.MD5, file.SHA1 = source.MD5, sum

	// The source may be deleted between the lookup and the new row: under the
	// per-blob lock it is looked up again, so the blob cannot go away before the
	// row that references it exists.
	h.commit(w, r, s, file, func(ctx context.Context) error {
		return storage.WithBlobLock(ctx, h.DB, file.SHA256, func(ctx context.Context) error {
			again, err := h.DB.FindConanPackageBySHA1(ctx, s.repoID, sum)
			if err != nil {
				return fmt.Errorf("find source file: %w", err)
			}

			if again == nil || again.SHA256 != file.SHA256 {
				return errChecksumMiss
			}

			if err := h.DB.CreateConanPackage(ctx, file); err != nil {
				return fmt.Errorf("create conan package: %w", err)
			}

			return nil
		})
	})
}

// reusableSource returns a live, non-quarantined file of the repository with
// the SHA-1, or nil.
func (h *HTTPHandler) reusableSource(r *http.Request, repoID, sum string) (*models.ConanPackage, error) {
	source, err := h.DB.FindConanPackageBySHA1(r.Context(), repoID, sum)
	if err != nil {
		return nil, fmt.Errorf("find by sha1: %w", err)
	}

	if source == nil {
		return nil, nil //nolint:nilnil // not found is (nil, nil), as in the Database contract
	}

	blocked, err := quarantine.IsQuarantined(r.Context(), h.DB, repoID, quarantineKeys(source)...)
	if err != nil {
		return nil, fmt.Errorf("check quarantine: %w", err)
	}

	if blocked {
		return nil, nil //nolint:nilnil // a quarantined source is never reused: the bytes must be uploaded and judged as a new file
	}

	return source, nil
}

// quarantineKeys are the identifiers a stored file may be quarantined under.
func quarantineKeys(f *models.ConanPackage) []string {
	resource := conanPath(f.Name, f.Version, f.UserChannel, "") + "/" + f.RecipeRevision
	if f.PackageID != "" {
		resource += "/" + f.PackageID + "/" + f.PackageRevision
	}

	return quarantine.PathKeys(
		f.Filename, conanPath(f.Name, f.Version, f.UserChannel, f.Filename), resource+"/"+f.Filename)
}

// keepDigests fills in the digests of a stored file that was re-uploaded with
// the same bytes and has none yet. Failures are logged: the upload succeeded.
func (h *HTTPHandler) keepDigests(ctx context.Context, stored, uploaded *models.ConanPackage) {
	if stored.SHA1 != "" && stored.MD5 != "" {
		return
	}

	if uploaded.MD5 == "" || uploaded.SHA1 == "" {
		return
	}

	if err := h.DB.SetConanPackageDigests(ctx, stored.ID, uploaded.MD5, uploaded.SHA1); err != nil {
		h.Log.Warnw("failed to record conan file digests", "id", stored.ID, "error", err)
		return
	}

	stored.MD5, stored.SHA1 = uploaded.MD5, uploaded.SHA1
}

// ensureDigests fills in the MD5 and SHA-1 of files stored before they were
// tracked, from the blobs, and records them so the work is done once.
func (h *HTTPHandler) ensureDigests(ctx context.Context, files []*models.ConanPackage) error {
	for _, f := range files {
		if f.MD5 != "" || f.BlobKey == "" {
			continue
		}

		md5Hex, sha1Hex, err := h.digestBlob(ctx, f.BlobKey)
		if err != nil {
			return fmt.Errorf("digest %s: %w", f.Filename, err)
		}

		if err := h.DB.SetConanPackageDigests(ctx, f.ID, md5Hex, sha1Hex); err != nil {
			return fmt.Errorf("record digests of %s: %w", f.Filename, err)
		}

		f.MD5, f.SHA1 = md5Hex, sha1Hex
	}

	return nil
}

// digestBlob streams a stored blob through MD5 and SHA-1.
func (h *HTTPHandler) digestBlob(ctx context.Context, blobKey string) (string, string, error) {
	rc, err := h.BlobStore.Get(ctx, blobKey)
	if err != nil {
		return "", "", fmt.Errorf("open blob: %w", err)
	}
	defer base.CloseLog(rc, h.Log)

	m, s := md5.New(), sha1.New() //nolint:gosec // see the import

	if _, err := io.Copy(io.MultiWriter(m, s), rc); err != nil {
		return "", "", fmt.Errorf("read blob: %w", err)
	}

	return hex.EncodeToString(m.Sum(nil)), hex.EncodeToString(s.Sum(nil)), nil
}
