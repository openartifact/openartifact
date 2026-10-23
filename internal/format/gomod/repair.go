// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gomod

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

// OpenArtifact <= 0.4.1 uploaded every Go module zip with an empty digest, so
// all gomod_modules rows pointed at ONE blobs row (sha256 = ''), whose object
// is the first zip ever uploaded. The real zips of later uploads were still
// written to the blob store under their true sha256, just without a blobs
// row. RepairZipBlobs recovers them: it matches the orphaned objects to the
// affected rows by their content and relinks each row under the true digest.

// repairListPage is the number of store keys inspected per List call.
const repairListPage = 1000

var errContentHash = errors.New("content does not hash to its key")

var errEntryPrefix = errors.New("zip entries do not share one <module>@<version>/ prefix")

// RepairDB is the database side of the repair; *database.PostgresDB implements it.
type RepairDB interface {
	TryGomodRepairLock(ctx context.Context) (func(), bool, error)
	ListGomodAffectedRows(ctx context.Context) ([]database.GomodAffectedRow, error)
	EmptySHA256BlobID(ctx context.Context) (string, error)
	ListGomodSiblingZipKeys(ctx context.Context) ([]string, error)
	UnreferencedBlobKeys(ctx context.Context, ids []string) ([]string, error)
	RelinkGomodZips(ctx context.Context, sha256 string, size int64, rowIDs []string) (int64, error)
	DeleteUnreferencedInvalidBlobRow(ctx context.Context, id string) (bool, error)
}

// RepairIssue names a module version the repair could not relink.
type RepairIssue struct {
	Repo    string
	Module  string
	Version string
	Reason  string
}

func (i RepairIssue) String() string {
	return fmt.Sprintf("%s: %s@%s (%s)", i.Repo, i.Module, i.Version, i.Reason)
}

// RepairResult summarises one repair run.
type RepairResult struct {
	// LockBusy is true when another replica is running the repair.
	LockBusy bool
	// Affected is the number of rows that pointed at an invalid blobs row.
	Affected int
	// Repaired is the number of rows relinked to their real zip.
	Repaired int
	// Unrepairable rows stay unservable (404) until re-uploaded.
	Unrepairable []RepairIssue
	// ListUnsupported is true when the blob store cannot enumerate objects
	// (OCI: the Distribution spec has no blob listing), so orphaned zips, which
	// no database row records, cannot be discovered and only the shared row's
	// own object could be recovered.
	ListUnsupported bool
	// BlobRowsDeleted counts invalid blobs rows removed from the database
	// (their storage objects are never deleted).
	BlobRowsDeleted int
}

// zipCandidate is a verified module zip found in the blob store.
type zipCandidate struct {
	key      string
	size     int64
	prefix   string // "<module>@<version>"
	goMod    string
	hasGoMod bool
}

// RepairZipBlobs repairs Go module rows left pointing at a blobs row whose
// sha256 is not a real digest (see the comment at the top of this file). It is
// idempotent and cheap when nothing is affected, and it holds a Postgres
// advisory lock on a pinned connection so only one replica runs at a time.
func RepairZipBlobs(ctx context.Context, db RepairDB, store storage.BlobStore, log *logger.Logger) (RepairResult, error) {
	var res RepairResult

	affected, err := db.ListGomodAffectedRows(ctx)
	if err != nil {
		return res, err
	}

	staleID, err := db.EmptySHA256BlobID(ctx)
	if err != nil {
		return res, err
	}

	if len(affected) == 0 && staleID == "" {
		return res, nil
	}

	release, ok, err := db.TryGomodRepairLock(ctx)
	if err != nil {
		return res, err
	}

	if !ok {
		res.LockBusy = true

		return res, nil
	}
	defer release()

	// Re-read under the lock: another replica may have just finished.
	if affected, err = db.ListGomodAffectedRows(ctx); err != nil {
		return res, err
	}

	res.Affected = len(affected)

	if len(affected) > 0 {
		if err := repairAffected(ctx, db, store, log, affected, &res); err != nil {
			return res, err
		}
	}

	if err := dropInvalidBlobRows(ctx, db, affected, &res); err != nil {
		return res, err
	}

	logRepairSummary(log, &res)

	return res, nil
}

func repairAffected(ctx context.Context, db RepairDB, store storage.BlobStore, log *logger.Logger,
	affected []database.GomodAffectedRow, res *RepairResult,
) error {
	cands, listUnsupported, err := collectCandidates(ctx, db, store, log, affected)
	if err != nil {
		return err
	}

	res.ListUnsupported = listUnsupported

	plan, issues := planRelinks(affected, cands)
	res.Unrepairable = issues

	for _, key := range slices.Sorted(mapKeys(plan)) {
		ids := plan[key]

		n, err := db.RelinkGomodZips(ctx, key, cands[key].size, ids)
		if err != nil {
			return fmt.Errorf("relink %d go module row(s) to zip %s: %w", len(ids), key, err)
		}

		res.Repaired += int(n)
	}

	return nil
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// collectCandidates gathers and verifies every zip that could belong to an
// affected row: the shared row's own object, same-release zips of healthy
// rows, and store objects that no blobs row references.
func collectCandidates(ctx context.Context, db RepairDB, store storage.BlobStore, log *logger.Logger,
	affected []database.GomodAffectedRow,
) (map[string]zipCandidate, bool, error) {
	keys := map[string]struct{}{}

	for _, a := range affected {
		if storage.ValidateBlobID(a.BadBlobKey) == nil {
			keys[a.BadBlobKey] = struct{}{}
		}
	}

	siblings, err := db.ListGomodSiblingZipKeys(ctx)
	if err != nil {
		return nil, false, err
	}

	for _, k := range siblings {
		keys[k] = struct{}{}
	}

	listUnsupported, err := addUnreferencedKeys(ctx, db, store, keys)
	if err != nil {
		return nil, false, err
	}

	cands := make(map[string]zipCandidate, len(keys))

	for _, key := range slices.Sorted(mapKeys(keys)) {
		c, err := inspectCandidate(ctx, store, key)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}

			log.Debugw("gomod repair: object is not a usable module zip", "key", key, "reason", err.Error())

			continue
		}

		cands[key] = c
	}

	return cands, listUnsupported, nil
}

// addUnreferencedKeys adds every store object that no blobs row references.
// It reports true when the store cannot list.
func addUnreferencedKeys(ctx context.Context, db RepairDB, store storage.BlobStore, keys map[string]struct{}) (bool, error) {
	token := ""

	for {
		ids, next, err := store.List(ctx, token, repairListPage)
		if errors.Is(err, storage.ErrListNotSupported) {
			return true, nil
		}

		if err != nil {
			return false, fmt.Errorf("list blob store: %w", err)
		}

		if len(ids) > 0 {
			orphans, err := db.UnreferencedBlobKeys(ctx, ids)
			if err != nil {
				return false, err
			}

			for _, k := range orphans {
				keys[k] = struct{}{}
			}
		}

		if next == "" {
			return false, nil
		}

		token = next
	}
}

// inspectCandidate reads the object (capped at the upload limit), checks that
// its content hashes to its key and that it is a module zip.
func inspectCandidate(ctx context.Context, store storage.BlobStore, key string) (zipCandidate, error) {
	rc, err := store.Get(ctx, key)
	if err != nil {
		return zipCandidate{}, fmt.Errorf("get: %w", err)
	}
	defer func() { _ = rc.Close() }()

	data, err := base.ReadAllLimited(rc, maxGoModUploadBytes)
	if err != nil {
		return zipCandidate{}, fmt.Errorf("read: %w", err)
	}

	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != key {
		return zipCandidate{}, errContentHash
	}

	c, err := analyzeModuleZip(data)
	if err != nil {
		return zipCandidate{}, err
	}

	c.key = key
	c.size = int64(len(data))

	return c, nil
}

// analyzeModuleZip requires every entry to live under one "<path>@<version>/"
// prefix and returns that prefix and the root go.mod, when there is one.
func analyzeModuleZip(data []byte) (zipCandidate, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return zipCandidate{}, fmt.Errorf("invalid zip file: %w", err)
	}

	prefix, err := commonEntryPrefix(zr.File)
	if err != nil {
		return zipCandidate{}, err
	}

	c := zipCandidate{prefix: prefix}

	if f := rootGoMod(zr.File); f != nil {
		rc, err := f.Open()
		if err != nil {
			return zipCandidate{}, fmt.Errorf("open go.mod: %w", err)
		}
		defer func() { _ = rc.Close() }()

		content, err := base.ReadAllLimited(rc, maxGoModFileBytes)
		if err != nil {
			return zipCandidate{}, fmt.Errorf("read go.mod: %w", err)
		}

		c.goMod, c.hasGoMod = string(content), true
	}

	return c, nil
}

// commonEntryPrefix returns the "<module>@<version>" every entry starts with.
func commonEntryPrefix(files []*zip.File) (string, error) {
	prefix := ""

	for i, f := range files {
		at := strings.IndexByte(f.Name, '@')
		if at < 0 {
			return "", errEntryPrefix
		}

		slash := strings.IndexByte(f.Name[at:], '/')
		if slash < 0 {
			return "", errEntryPrefix
		}

		p := f.Name[:at+slash]
		if i == 0 {
			prefix = p
		} else if p != prefix {
			return "", errEntryPrefix
		}
	}

	if prefix == "" {
		return "", errEntryPrefix
	}

	return prefix, nil
}

// planRelinks matches rows to candidates. A row matches a candidate when the
// zip's prefix is module_path@version and its go.mod equals the row's
// go_mod_content (or the zip has none). Exactly one distinct match relinks the
// row; none or several leave it unrepaired (and unservable).
func planRelinks(affected []database.GomodAffectedRow, cands map[string]zipCandidate) (map[string][]string, []RepairIssue) {
	plan := map[string][]string{}

	var issues []RepairIssue

	for _, a := range affected {
		want := a.ModulePath + "@" + a.Version

		var matches []string

		for key, c := range cands {
			if c.prefix == want && (!c.hasGoMod || c.goMod == a.GoModContent) {
				matches = append(matches, key)
			}
		}

		switch len(matches) {
		case 0:
			issues = append(issues, RepairIssue{a.RepoName, a.ModulePath, a.Version, "no stored zip matches; re-upload this version"})
		case 1:
			plan[matches[0]] = append(plan[matches[0]], a.ID)
		default:
			issues = append(issues, RepairIssue{a.RepoName, a.ModulePath, a.Version,
				fmt.Sprintf("ambiguous: %d distinct stored zips match; re-upload this version", len(matches))})
		}
	}

	return plan, issues
}

// dropInvalidBlobRows deletes, from the database only, the invalid blobs rows
// the affected rows pointed at (and the stale "" row) once nothing references
// them. Their storage objects are real zips and are never deleted.
func dropInvalidBlobRows(ctx context.Context, db RepairDB, affected []database.GomodAffectedRow, res *RepairResult) error {
	ids := map[string]struct{}{}

	for _, a := range affected {
		ids[a.BadBlobID] = struct{}{}
	}

	emptyID, err := db.EmptySHA256BlobID(ctx)
	if err != nil {
		return err
	}

	if emptyID != "" {
		ids[emptyID] = struct{}{}
	}

	for _, id := range slices.Sorted(mapKeys(ids)) {
		deleted, err := db.DeleteUnreferencedInvalidBlobRow(ctx, id)
		if err != nil {
			return err
		}

		if deleted {
			res.BlobRowsDeleted++
		}
	}

	return nil
}

func logRepairSummary(log *logger.Logger, res *RepairResult) {
	if res.ListUnsupported {
		log.Warnw("gomod repair: the storage backend cannot list objects; rows whose zip is not the shared row's own object stay unservable until re-uploaded")
	}

	for _, i := range res.Unrepairable {
		log.Warnw("gomod repair: version left unrepaired, it answers 404 until re-uploaded",
			"repo", i.Repo, "module", i.Module, "version", i.Version, "reason", i.Reason)
	}

	log.Infow("gomod repair finished",
		"affected", res.Affected,
		"repaired", res.Repaired,
		"unrepairable", len(res.Unrepairable),
		"blob_rows_deleted", res.BlobRowsDeleted)
}

var _ RepairDB = (*database.PostgresDB)(nil)
