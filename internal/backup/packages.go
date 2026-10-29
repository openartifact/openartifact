// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/storage"
)

// dockerFormat is the one format that keeps dedicated tables instead of the
// generic package model; backup carries it through its typed docker tables.
const dockerFormat = "docker"

// packageRowsDir is the archive directory of the generic package rows. One
// entry per repository and table, <dir>/<old repository id>/<table>.jsonl, in
// restore order, every line one row.
const packageRowsDir = "package_rows/"

// ErrNoStorageDescriptor reports a repository whose format has no storage
// descriptor: its package rows cannot be archived or restored, and a backup
// that silently left them out would look complete.
var ErrNoStorageDescriptor = errors.New("format has no storage descriptor")

// packageRowSets lists the generic tables of a repository of the format
// described by d, in restore order: companion tables first, then packages,
// versions, files, side tables and tags.
func packageRowSets(d storage.Storage) []database.PackageRowSet {
	sets := make([]database.PackageRowSet, 0, len(d.Backup.Companions)+len(d.SideTables)+4)

	for _, c := range d.Backup.Companions {
		sets = append(sets, database.PackageRowSet{
			Kind: database.PackageRowsCompanion, Table: c.Name, Scope: c.Scope, RepoColumn: c.RepoColumn, Refs: c.Refs,
		})
	}

	sets = append(sets,
		database.PackageRowSet{Kind: database.PackageRowsPackages},
		database.PackageRowSet{Kind: database.PackageRowsVersions},
		database.PackageRowSet{Kind: database.PackageRowsFiles},
	)

	for _, t := range d.SideTables {
		sets = append(sets, database.PackageRowSet{Kind: database.PackageRowsSide, Table: t.Name, Refs: t.Refs})
	}

	return append(sets, database.PackageRowSet{Kind: database.PackageRowsTags})
}

// exportPackageRows writes the generic package rows of every repository that
// has them: every repository but docker's.
func (x *exporter) exportPackageRows(ctx context.Context, tw *tar.Writer, manifest *BackupManifest, repos []*models.Repository) error {
	for _, repo := range repos {
		if repo.Format == dockerFormat {
			continue
		}

		d, ok := storage.Lookup(repo.Format)
		if !ok {
			return fmt.Errorf("%w: %q (repository %s)", ErrNoStorageDescriptor, repo.Format, repo.Name)
		}

		for _, set := range packageRowSets(d) {
			if err := x.writePackageRows(ctx, tw, manifest, repo.ID, set); err != nil {
				return err
			}
		}
	}

	return nil
}

// writePackageRows archives one table of one repository. The tar header needs
// the entry size up front, so the rows are spooled to a temp file (never held
// in memory: a repository can have millions of file rows).
func (x *exporter) writePackageRows(
	ctx context.Context,
	tw *tar.Writer,
	manifest *BackupManifest,
	repoID string,
	set database.PackageRowSet,
) error {
	spool, err := os.CreateTemp("", "backup-rows-*.jsonl")
	if err != nil {
		return fmt.Errorf("failed to create row spool file: %w", err)
	}

	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()

	w := bufio.NewWriter(spool)
	rows := 0

	err = x.db.ExportPackageRows(ctx, set, repoID, func(row []byte) error {
		rows++

		if _, err := w.Write(row); err != nil {
			return fmt.Errorf("failed to spool row: %w", err)
		}

		return w.WriteByte('\n')
	})
	if err != nil {
		return fmt.Errorf("failed to export %s of repository %s: %w", set.Name(), repoID, err)
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("failed to spool %s: %w", set.Name(), err)
	}

	if rows == 0 {
		return nil
	}

	size, err := spool.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("failed to size row spool: %w", err)
	}

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to rewind row spool: %w", err)
	}

	header := &tar.Header{Name: packageRowsDir + repoID + "/" + set.Name() + ".jsonl", Size: size, Mode: 0o644}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("failed to write %s header: %w", header.Name, err)
	}

	if _, err := io.Copy(tw, spool); err != nil {
		return fmt.Errorf("failed to write %s: %w", header.Name, err)
	}

	manifest.TableCounts[set.Name()] += rows

	return nil
}

// restorePackageRows restores one package_rows entry into the repository the
// archive's repository id was restored as. The table is picked from the
// repository's descriptor, never taken from the entry name, so an entry can
// only target the tables the format declares.
func (e *Engine) restorePackageRows(ctx context.Context, r io.Reader, name string, st *restoreState) error {
	repo, d, set := e.resolveRowSet(name, st)
	if set == nil {
		return nil
	}

	br := bufio.NewReader(io.LimitReader(ctxReader{ctx: ctx, r: r}, maxTableSize))
	batch := make([]json.RawMessage, 0, database.PackageRestoreBatch)
	flush := func() {
		if len(batch) > 0 {
			e.restoreBatch(ctx, st, repo, d, *set, batch)
			batch = batch[:0]
		}
	}

	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			batch = append(batch, line)
			if len(batch) == database.PackageRestoreBatch {
				flush()
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("failed to read %s: %w", name, err)
		}
	}

	flush()

	return nil
}

// resolveRowSet maps a package_rows entry to the restored repository, its
// descriptor and the row set the entry names; set is nil (and the entry
// counted as a failure) when any of them is unknown.
func (e *Engine) resolveRowSet(name string, st *restoreState) (*models.Repository, storage.Storage, *database.PackageRowSet) {
	oldRepoID, table, ok := strings.Cut(strings.TrimPrefix(name, packageRowsDir), "/")
	table = strings.TrimSuffix(table, ".jsonl")

	repo := st.restoredRepos[oldRepoID]
	if !ok || repo == nil {
		e.restoreWarn("skipping package rows: source repository not restored", "entry", name)

		return nil, storage.Storage{}, nil
	}

	d, found := storage.Lookup(repo.Format)
	if !found {
		e.restoreWarn("skipping package rows: format has no storage descriptor", "entry", name, "format", repo.Format)

		return nil, storage.Storage{}, nil
	}

	sets := packageRowSets(d)
	for i := range sets {
		if sets[i].Name() == table {
			return repo, d, &sets[i]
		}
	}

	e.restoreWarn("skipping package rows: the repository's format does not have this table", "entry", name, "format", repo.Format)

	return nil, storage.Storage{}, nil
}

// restoreBatch inserts one batch of rows of set into repo and notes what the
// later rows and the post-restore regeneration need from it.
func (e *Engine) restoreBatch(
	ctx context.Context, st *restoreState, repo *models.Repository, d storage.Storage, set database.PackageRowSet, batch []json.RawMessage,
) {
	switch {
	case set.Kind == database.PackageRowsPackages && d.Backup.Regen == storage.RegenMaven:
		st.collectMavenCoords(repo.ID, batch)
	case set.Kind == database.PackageRowsCompanion:
		st.collectCompanionIDs(repo.ID, batch)
	case (set.Kind == database.PackageRowsVersions || set.Kind == database.PackageRowsFiles) && len(st.companionIDs[repo.ID]) > 0:
		for i, row := range batch {
			batch[i] = embedRestoredIDs(row, repo.ID, st.companionIDs[repo.ID])
		}
	}

	if failed, err := e.db.RestorePackageRows(ctx, set, repo.ID, batch); failed > 0 {
		st.failures += failed
		e.log.Warnw("failed to restore package rows", "table", set.Name(), "repo", repo.Name, "failed", failed, "error", err)
	}
}

// collectCompanionIDs notes the archived ids of restored companion rows: the
// version key and partition of the repository's rows may embed them.
func (st *restoreState) collectCompanionIDs(repoID string, rows []json.RawMessage) {
	for _, row := range rows {
		var c struct {
			ID string `json:"id"`
		}

		if json.Unmarshal(row, &c) == nil && c.ID != "" {
			st.companionIDs[repoID] = append(st.companionIDs[repoID], c.ID)
		}
	}
}

// embedRestoredIDs rewrites the archived companion ids a version's version_key
// or a file's part embeds (deb: component and distribution ids) to the ids the
// restore gave those companions. A row it cannot decode is returned as it was;
// the insert reports it.
func embedRestoredIDs(row json.RawMessage, repoID string, oldIDs []string) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(row, &fields) != nil {
		return row
	}

	changed := false

	for _, col := range []string{"version_key", "part"} {
		var v string
		if json.Unmarshal(fields[col], &v) != nil || v == "" {
			continue
		}

		was := v

		for _, old := range oldIDs {
			v = strings.ReplaceAll(v, old, database.RestoredRowID(repoID, old))
		}

		if v == was {
			continue
		}

		changed = true

		enc, err := json.Marshal(v)
		if err != nil {
			return row
		}

		fields[col] = enc
	}

	if !changed {
		return row
	}

	out, err := json.Marshal(fields)
	if err != nil {
		return row
	}

	return out
}

// collectMavenCoords notes the group and artifact of restored maven packages
// (packages.attrs.group_id and artifact_id), the units maven-metadata.xml is
// generated per.
func (st *restoreState) collectMavenCoords(repoID string, rows []json.RawMessage) {
	for _, row := range rows {
		var p struct {
			Attrs struct {
				GroupID    string `json:"group_id"`
				ArtifactID string `json:"artifact_id"`
			} `json:"attrs"`
		}

		if json.Unmarshal(row, &p) != nil || p.Attrs.GroupID == "" || p.Attrs.ArtifactID == "" {
			continue
		}

		if st.mavenCoords[repoID] == nil {
			st.mavenCoords[repoID] = map[[2]string]struct{}{}
		}

		st.mavenCoords[repoID][[2]string{p.Attrs.GroupID, p.Attrs.ArtifactID}] = struct{}{}
	}
}

// regenerateRestoredMetadata makes the generated metadata of restored hosted
// repositories servable. Backups carry rows only; repodata, dists and
// maven-metadata.xml are derived, and nothing else would create them until a
// new upload hits the same repository. What a repository needs is the Regen
// kind of its format's descriptor.
func (e *Engine) regenerateRestoredMetadata(ctx context.Context, st *restoreState) {
	for _, repo := range st.restoredRepos {
		if repo.RepoType == "proxy" || repo.RepoType == "group" {
			continue
		}

		d, ok := storage.Lookup(repo.Format)
		if !ok {
			continue
		}

		switch d.Backup.Regen {
		case storage.RegenRPM:
			// rpm gets its rpm_repos row (the worker's only index of work) marked
			// stale, so the periodic stale sweep also covers a missing notifier.
			e.regenerateRestoredRPM(ctx, repo)
		case storage.RegenDeb:
			e.notifyRestored(func(r Regenerator) { r.NotifyDebStale(repo.ID) })
		case storage.RegenPacman:
			e.notifyRestored(func(r Regenerator) { r.NotifyPacmanStale(repo.ID) })
		case storage.RegenApk:
			e.notifyRestored(func(r Regenerator) { r.NotifyApkStale(repo.ID) })
		case storage.RegenMaven:
			for coord := range st.mavenCoords[repo.ID] {
				e.notifyRestored(func(r Regenerator) { r.NotifyMavenStale(repo.ID, coord[0], coord[1]) })
			}
		case storage.RegenNPM:
			// the packument is rendered per request from the rows
		}
	}
}

func (e *Engine) notifyRestored(notify func(Regenerator)) {
	if e.regen != nil {
		notify(e.regen)
	}
}

func (e *Engine) regenerateRestoredRPM(ctx context.Context, repo *models.Repository) {
	existing, err := e.db.GetRPMRepo(ctx, repo.ID)
	if err != nil {
		e.restoreWarn("failed to look up rpm repo after restore", "repo", repo.Name, "error", err)
		return
	}

	if existing == nil {
		if err := e.db.CreateRPMRepo(ctx, &models.RPMRepo{RepositoryID: repo.ID, BaseArch: "x86_64"}); err != nil {
			e.restoreWarn("failed to create rpm repo after restore", "repo", repo.Name, "error", err)
			return
		}
	}

	if err := e.db.MarkRepoStale(ctx, repo.ID); err != nil {
		e.restoreWarn("failed to mark rpm repo stale after restore", "repo", repo.Name, "error", err)
		return
	}

	e.notifyRestored(func(r Regenerator) { r.NotifyRPMStale(repo.ID) })
}
