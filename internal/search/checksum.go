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
	// Filename is the file within the package, "" for docker.
	Filename  string `json:"filename"`
	Checksum  string `json:"checksum"`
	Algorithm string `json:"algorithm"`
}

// ParseChecksum normalises a checksum query value (lowercase hex of a SHA-256,
// SHA-1 or MD5 digest) and names its algorithm; ok is false for anything else.
func ParseChecksum(value string) (string, string, bool) {
	digest := strings.ToLower(strings.TrimSpace(value))

	var algorithm string

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
// version, filename), and is exact on an indexed digest. The digest is $1; a
// sha256 lookup also binds the blob row ($2) and the docker manifest digest ($3).
const (
	// checksumFileFrom is the generic file/version/package join every
	// package_files branch reads.
	checksumFileFrom = `FROM package_files f
		JOIN package_versions v ON v.id = f.version_id
		JOIN packages p ON p.id = v.package_id`

	// SHA-256 is a lookup on the blobs table (unique on sha256) plus one probe of
	// idx_package_files_blob: every live file stored under that content, listed
	// or not (a go.mod, a pom).
	checksumBlobBranch = `SELECT f.repository_id, p.format::text AS format, p.name::text AS name, v.version::text AS version,
		f.filename::text AS filename
		` + checksumFileFrom + `
		WHERE f.blob_id = $2::uuid AND f.deleted_at IS NULL`

	// The docker manifest digest and the proxy cache stay special cases: docker
	// has its own tables and proxy_cache_entries is not a package table.
	checksumSHA256Extras = `SELECT dt.repository_id, 'docker'::text, dt.name, ''::text, ''::text
		FROM docker_manifests dm
		JOIN docker_tags dt ON dt.repository_id = dm.repository_id AND dt.image = dm.image
			AND dt.manifest_digest = dm.digest AND dt.deleted_at IS NULL
		WHERE dm.digest = $3 AND dm.deleted_at IS NULL
	UNION ALL
	SELECT pc.repository_id, pr.format::text, pc.cache_key, ''::text, pc.cache_key
		FROM proxy_cache_entries pc
		JOIN repositories pr ON pr.id = pc.repository_id AND pr.deleted_at IS NULL
		WHERE pc.blob_key = $1 AND pc.status_code = 200`
)

// checksumKeyBranch is a lookup of $1 in checksums->>key. The "checksums ? key
// AND deleted_at IS NULL" predicate is the one of the partial expression index
// (idx_package_files_sha1/_md5/_shasum), which makes it an index probe.
func checksumKeyBranch(key string) string {
	return `SELECT f.repository_id, p.format::text AS format, p.name::text AS name, v.version::text AS version,
		f.filename::text AS filename
		` + checksumFileFrom + `
		WHERE (f.checksums ->> '` + key + `') = $1 AND f.checksums ? '` + key + `' AND f.deleted_at IS NULL`
}

// SearchChecksum finds the files whose stored checksum is digest (see
// ParseChecksum), in the readableRepos only (scoped in SQL: an empty set always
// yields nothing), ordered by repository, name, version and file name. It returns
// the page at offset and the number of matches, or ErrChecksumTooBroad.
//
// A SHA-256 matches through the blob store, so it finds every file stored under
// that content, whichever format holds it. A SHA-1 or MD5 only matches the
// files that record one in package_files.checksums (the sha1, shasum and md5
// keys, each backed by a partial expression index): the blob store keeps only
// SHA-256. Other digests of that column (sha512, integrity) have no index in
// the baseline and are not searchable.
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
		union = checksumBlobBranch + "\n\tUNION ALL\n\t" + checksumSHA256Extras
	case AlgorithmSHA1:
		// maven, conan and the like record "sha1"; npm records its SHA-1 as "shasum".
		union = checksumKeyBranch("sha1") + "\n\tUNION ALL\n\t" + checksumKeyBranch("shasum")
	default:
		union = checksumKeyBranch("md5")
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
