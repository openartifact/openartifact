// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Checksum algorithms, named by the length of their hex digests.
const (
	AlgorithmSHA256 = "sha256"
	AlgorithmSHA1   = "sha1"
	AlgorithmMD5    = "md5"

	// maxChecksumResults bounds the matches of one checksum lookup. A digest
	// names one file, so it is only ever matched by the copies of that file;
	// more than this is a pathological store, not a search to page through.
	maxChecksumResults = 500
)

// ErrChecksumTooBroad is returned when a digest matches more than
// maxChecksumResults files.
var ErrChecksumTooBroad = errors.New("checksum matches too many files")

// ChecksumResult is a file whose stored checksum equals the one searched for.
type ChecksumResult struct {
	Repository string `json:"repository"`
	Format     string `json:"format"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	// Filename is the file within the package, "" for the formats that have no
	// file name of their own (conda, conan, docker, go).
	Filename  string `json:"filename"`
	Checksum  string `json:"checksum"`
	Algorithm string `json:"algorithm"`
}

// ParseChecksum normalises a checksum query value (lowercase hex of a SHA-256,
// SHA-1 or MD5 digest) and names its algorithm; ok is false for anything else.
func ParseChecksum(value string) (digest, algorithm string, ok bool) {
	digest = strings.ToLower(strings.TrimSpace(value))

	switch len(digest) {
	case 64:
		algorithm = AlgorithmSHA256
	case 40:
		algorithm = AlgorithmSHA1
	case 32:
		algorithm = AlgorithmMD5
	default:
		return "", "", false
	}

	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", "", false
		}
	}

	return digest, algorithm, true
}

// Branches of the checksum lookup: each yields (repository_id, format, name,
// version, filename), and is exact on an indexed digest column. The digest is
// $1; a sha256 lookup also binds the blob row ($2) and the docker manifest
// digest ($3).
const (
	// The package tables reach their blob through blob_id (indexed in every
	// table of the search view), so SHA-256 is a lookup on the blobs table
	// (unique on sha256) plus one index probe per table.
	checksumBlobBranch = `SELECT s.repository_id, s.format, s.name, s.version, s.path AS filename
		FROM ` + "%s" + ` AS s WHERE s.blob_id = $2::uuid`

	checksumSHA256Extras = `SELECT gm.repository_id, 'go'::text, gm.module_path, gm.version, gm.version || '.mod'
		FROM gomod_modules gm WHERE gm.mod_blob_id = $2::uuid AND gm.deleted_at IS NULL
	UNION ALL
	SELECT dt.repository_id, 'docker'::text, dt.name, ''::text, ''::text
		FROM docker_manifests dm
		JOIN docker_tags dt ON dt.repository_id = dm.repository_id AND dt.image = dm.image
			AND dt.manifest_digest = dm.digest AND dt.deleted_at IS NULL
		WHERE dm.digest = $3 AND dm.deleted_at IS NULL
	UNION ALL
	SELECT pc.repository_id, pr.format::text, pc.cache_key, ''::text, pc.cache_key
		FROM proxy_cache_entries pc
		JOIN repositories pr ON pr.id = pc.repository_id AND pr.deleted_at IS NULL
		WHERE pc.blob_key = $1 AND pc.status_code = 200`

	checksumSHA1Branches = `SELECT ma.repository_id, 'maven'::text, ma.group_id || ':' || ma.artifact_id_coord, ma.version, ma.repo_path
		FROM maven_artifacts ma WHERE ma.sha1 = $1 AND ma.sha1 <> '' AND ma.deleted_at IS NULL
	UNION ALL
	SELECT np.repository_id, 'npm'::text, np.full_name, nv.version, nv.tarball_filename
		FROM npm_versions nv JOIN npm_packages np ON np.id = nv.package_id
		WHERE nv.shasum = $1 AND nv.shasum <> '' AND nv.deleted_at IS NULL
	UNION ALL
	SELECT co.repository_id, 'conan'::text, co.name, co.version, co.filename
		FROM conan_packages co WHERE co.sha1 = $1 AND co.sha1 <> '' AND co.deleted_at IS NULL`

	checksumMD5Branches = `SELECT ma.repository_id, 'maven'::text, ma.group_id || ':' || ma.artifact_id_coord, ma.version, ma.repo_path
		FROM maven_artifacts ma WHERE ma.md5 = $1 AND ma.md5 <> '' AND ma.deleted_at IS NULL
	UNION ALL
	SELECT co.repository_id, 'conan'::text, co.name, co.version, co.filename
		FROM conan_packages co WHERE co.md5 = $1 AND co.md5 <> '' AND co.deleted_at IS NULL
	UNION ALL
	SELECT cp.repository_id, 'conda'::text, cp.name, cp.version, ''::text
		FROM conda_packages cp WHERE cp.md5 = $1 AND cp.md5 <> '' AND cp.deleted_at IS NULL`
)

// SearchChecksum finds the files whose stored checksum is digest (see
// ParseChecksum), in the readableRepos only (scoped in SQL: an empty set always
// yields nothing), ordered by repository, name, version and file name. It returns
// the page at offset and the number of matches, or ErrChecksumTooBroad.
//
// A SHA-256 matches through the blob store, so it finds every file stored under
// that content, whichever format holds it. A SHA-1 or MD5 only matches the
// formats that record one (maven, npm and conan record SHA-1, maven, conan and
// conda MD5): the blob store keeps only SHA-256.
func (e *Executor) SearchChecksum(
	ctx context.Context,
	digest, algorithm string,
	limit, offset int,
	readableRepos map[uuid.UUID]struct{},
) ([]ChecksumResult, int, error) {
	if len(readableRepos) == 0 {
		return nil, 0, nil
	}

	var b whereBuilder

	b.args = []any{digest}

	var union string

	switch algorithm {
	case AlgorithmSHA256:
		blobID, err := e.blobIDBySHA256(ctx, digest)
		if err != nil {
			return nil, 0, err
		}

		if blobID == "" {
			return nil, 0, nil
		}

		b.args = append(b.args, blobID, "sha256:"+digest)
		union = fmt.Sprintf(checksumBlobBranch, narrowSearchView()) + "\n\tUNION ALL\n\t" + checksumSHA256Extras
	case AlgorithmSHA1:
		union = checksumSHA1Branches
	default:
		union = checksumMD5Branches
	}

	b.addReadable("x.repository_id", readableRepos)

	//nolint:gosec // G201: the branches are constant fragments and every value is a bound argument
	q := fmt.Sprintf(`SELECT r.name, x.format, x.name, x.version, x.filename
	FROM (
	%s
	) AS x%s
	JOIN repositories r ON r.id = x.repository_id AND r.deleted_at IS NULL
	%s
	ORDER BY r.name, x.name, x.version, x.filename
	LIMIT %d`, union, b.join, whereClause(b.String()), maxChecksumResults+1)

	rows, err := e.db.QueryContext(ctx, q, b.args...)
	if err != nil {
		return nil, 0, fmt.Errorf("checksum query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var all []ChecksumResult

	for rows.Next() {
		res := ChecksumResult{Checksum: digest, Algorithm: algorithm}

		if err := rows.Scan(&res.Repository, &res.Format, &res.Name, &res.Version, &res.Filename); err != nil {
			return nil, 0, fmt.Errorf("scan checksum result: %w", err)
		}

		all = append(all, res)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("checksum query: %w", err)
	}

	if len(all) > maxChecksumResults {
		return nil, 0, ErrChecksumTooBroad
	}

	start := min(offset, len(all))

	return all[start:min(start+limit, len(all))], len(all), nil
}

// blobIDBySHA256 resolves a SHA-256 to its blobs row; "" when the store holds
// no such content.
func (e *Executor) blobIDBySHA256(ctx context.Context, digest string) (string, error) {
	var id string

	err := e.db.QueryRowContext(ctx, `SELECT id::text FROM blobs WHERE sha256 = $1`, digest).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("blob lookup: %w", err)
	}

	return id, nil
}
