// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	sq "github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/pkg/pacman"
)

// PacmanMetadataWorker regenerates the pacman databases of hosted pacman
// repositories: for each architecture directory the "db" and "files" archives
// (repo-add layout), signed with the repository key, and the public key. It
// also signs, with the same key, the packages the uploader did not sign. Runs
// are coalesced by the regeneration queue.
type PacmanMetadataWorker struct {
	db        sq.Database
	blobStore storage.BlobStore
	signers   gpg.Resolver
	log       *logger.Logger
	q         *regenQueue[string]
	onStale   func(repoID string)
}

// NewPacmanMetadataWorker creates the worker.
func NewPacmanMetadataWorker(db sq.Database, blobStore storage.BlobStore, signers gpg.Resolver, log *logger.Logger) *PacmanMetadataWorker {
	w := &PacmanMetadataWorker{db: db, blobStore: blobStore, signers: signers, log: log}
	w.q = newRegenQueue("pacman", log, w.regenerate)

	return w
}

// WithDebounce sets how long a burst of uploads is coalesced.
func (w *PacmanMetadataWorker) WithDebounce(d time.Duration) *PacmanMetadataWorker {
	w.q.setDebounce(d)
	return w
}

// WithMetrics records regeneration counts and durations in reg.
func (w *PacmanMetadataWorker) WithMetrics(reg *metrics.Registry) *PacmanMetadataWorker {
	w.q.setMetrics(reg)
	return w
}

// WithDrainTimeout sets how long Stop waits for pending regenerations.
func (w *PacmanMetadataWorker) WithDrainTimeout(d time.Duration) *PacmanMetadataWorker {
	w.q.setDrainTimeout(d)
	return w
}

// WithRegenLimiter shares a concurrency limiter with the other metadata workers.
func (w *PacmanMetadataWorker) WithRegenLimiter(l *RegenLimiter) *PacmanMetadataWorker {
	w.q.setLimiter(l)
	return w
}

// WithStaleHook calls fn with the repository id on every NotifyStale: the group
// repositories that list it as a member regenerate their merged databases.
func (w *PacmanMetadataWorker) WithStaleHook(fn func(repoID string)) *PacmanMetadataWorker {
	w.onStale = fn
	return w
}

// NotifyStale schedules a coalesced regeneration of the repository's databases.
func (w *PacmanMetadataWorker) NotifyStale(repoID string) {
	w.q.Notify(repoID)

	if w.onStale != nil {
		w.onStale(repoID)
	}
}

// Stop drains pending regenerations and waits for running ones (bounded).
func (w *PacmanMetadataWorker) Stop() { w.q.Stop() }

func (w *PacmanMetadataWorker) blobs() metadataBlobs {
	return metadataBlobs{db: w.db, store: w.blobStore, log: w.log, who: "pacman worker"}
}

func (w *PacmanMetadataWorker) regenerate(ctx context.Context, repoID string) error {
	if sqlDB := w.db.GetDB(); sqlDB != nil {
		release, err := lockForRegeneration(ctx, sqlDB, "pacman:"+repoID)
		if err != nil {
			return fmt.Errorf("pacman worker: repository %s: %w", repoID, err)
		}

		defer release()
	}

	signer, err := metadataSigner(ctx, w.signers, repoID, "pacman worker")
	if err != nil {
		return err
	}

	if err := w.fillFileLists(ctx, repoID); err != nil {
		return err
	}

	pkgs, err := w.db.ListPacmanPackages(ctx, repoID)
	if err != nil {
		return fmt.Errorf("pacman worker: listing packages: %w", err)
	}

	resigned, err := w.signPackages(ctx, repoID, signer, pkgs)
	if err != nil {
		return err
	}

	lists, err := w.db.ListPacmanFileLists(ctx, repoID)
	if err != nil {
		return fmt.Errorf("pacman worker: listing file lists: %w", err)
	}

	blobs, err := renderPacmanDatabases(pkgs, lists, signer)
	if err != nil {
		return err
	}

	// Clients fetch the key the databases and packages are signed with from
	// HOST/repos/REPO/public.key, like APT and RPM clients.
	if signer != nil {
		publicKey, keyErr := signer.PublicKey()
		if keyErr != nil {
			return fmt.Errorf("pacman worker: exporting public key: %w", keyErr)
		}

		blobs["public.key"] = publicKey
	}

	if err := w.publish(ctx, repoID, blobs); err != nil {
		return err
	}

	// The groups listing this repository carry the package signatures in their
	// databases: tell them about the ones this run made, which their regeneration
	// for the upload itself may have run before.
	if resigned && w.onStale != nil {
		w.onStale(repoID)
	}

	return nil
}

// fillFileLists reads the file list of every package that has none from its
// blob. Packages uploaded here have it from the start; restored or promoted
// rows do not. An unreadable package is logged and left for the next run: its
// database entry is written without a file list.
func (w *PacmanMetadataWorker) fillFileLists(ctx context.Context, repoID string) error {
	missing, err := w.db.ListPacmanPackagesWithoutFileList(ctx, repoID)
	if err != nil {
		return fmt.Errorf("pacman worker: listing packages without file list: %w", err)
	}

	for _, pkg := range missing {
		files, err := w.readFileList(ctx, pkg.BlobKey)
		if err != nil {
			w.log.Warnw("pacman worker: cannot read the file list of a package", "repo_id", repoID, "filename", pkg.Filename, "error", err)
			continue
		}

		if err := w.db.SetPacmanFileList(ctx, repoID, pkg.ID, files); err != nil {
			return fmt.Errorf("pacman worker: storing file list: %w", err)
		}
	}

	return nil
}

func (w *PacmanMetadataWorker) readFileList(ctx context.Context, blobKey string) (string, error) {
	rc, err := w.blobStore.Get(ctx, blobKey)
	if err != nil {
		return "", fmt.Errorf("open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()

	pkg, err := pacman.ReadPackage(rc)
	if err != nil {
		return "", fmt.Errorf("read package: %w", err)
	}

	return strings.Join(pkg.Files, "\n"), nil
}

// signPackages makes the package signatures consistent with the repository
// key: a package without an uploader signature is signed with the key when it
// has none or was signed by another key, and loses a server signature when the
// repository has no key any more. The signatures are updated in pkgs too, and
// changed reports whether any was.
func (w *PacmanMetadataWorker) signPackages(
	ctx context.Context,
	repoID string,
	signer gpg.Signer,
	pkgs []*models.PacmanPackage,
) (bool, error) {
	fingerprint := ""
	if signer != nil {
		fingerprint = signer.KeyFingerprint()
	}

	changed := false

	for _, pkg := range pkgs {
		if pkg.SignatureSource == models.PacmanSigUploader {
			continue
		}

		if signer == nil {
			if pkg.Signature == "" {
				continue
			}

			if err := w.setSignature(ctx, repoID, pkg, "", "", ""); err != nil {
				return changed, err
			}

			changed = true

			continue
		}

		if pkg.Signature != "" && pkg.SignatureKey == fingerprint {
			continue
		}

		sig, err := w.signBlob(ctx, signer, pkg.BlobKey)
		if err != nil {
			return changed, fmt.Errorf("pacman worker: signing %s: %w", pkg.Filename, err)
		}

		if err := w.setSignature(
			ctx,
			repoID,
			pkg,
			base64.StdEncoding.EncodeToString(sig),
			models.PacmanSigServer,
			fingerprint,
		); err != nil {
			return changed, err
		}

		changed = true
	}

	return changed, nil
}

func (w *PacmanMetadataWorker) setSignature(ctx context.Context, repoID string, pkg *models.PacmanPackage, sig, source, key string) error {
	if err := w.db.SetPacmanSignature(ctx, repoID, pkg.ID, sig, source, key); err != nil {
		return fmt.Errorf("pacman worker: storing signature of %s: %w", pkg.Filename, err)
	}

	pkg.Signature, pkg.SignatureSource, pkg.SignatureKey = sig, source, key

	return nil
}

func (w *PacmanMetadataWorker) signBlob(ctx context.Context, signer gpg.Signer, blobKey string) ([]byte, error) {
	rc, err := w.blobStore.Get(ctx, blobKey)
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()

	return pacman.SignDetached(signer, rc)
}

// renderPacmanDatabases renders, for every architecture directory that has a
// package, os/ARCH/db.tar.gz and os/ARCH/files.tar.gz and, with a signer,
// their .sig files.
func renderPacmanDatabases(pkgs []*models.PacmanPackage, lists map[string]string, signer gpg.Signer) (map[string][]byte, error) {
	arches := make(map[string]struct{})
	for _, p := range pkgs {
		arches[p.RepoArch] = struct{}{}
	}

	blobs := make(map[string][]byte, len(arches)*4)

	for arch := range arches {
		if err := renderArchDatabases(blobs, arch, pacmanEntries(pkgs, lists, arch), signer); err != nil {
			return nil, err
		}
	}

	return blobs, nil
}

// pacmanArchPath is where the generated document doc ("db.tar.gz",
// "files.tar.gz", with or without ".sig") of an architecture is stored.
func pacmanArchPath(arch, doc string) string { return "os/" + arch + "/" + doc }

// renderArchDatabases adds the db and files databases of one architecture
// directory, and with a signer their signatures, to blobs.
func renderArchDatabases(blobs map[string][]byte, arch string, entries []pacman.DBEntry, signer gpg.Signer) error {
	for _, doc := range []struct {
		name      string
		withFiles bool
	}{{"db.tar.gz", false}, {"files.tar.gz", true}} {
		var buf bytes.Buffer
		if err := pacman.WriteDB(&buf, entries, doc.withFiles); err != nil {
			return fmt.Errorf("pacman worker: writing %s of %s: %w", doc.name, arch, err)
		}

		path := pacmanArchPath(arch, doc.name)
		blobs[path] = buf.Bytes()

		if signer == nil {
			continue
		}

		sig, err := pacman.SignDetached(signer, bytes.NewReader(buf.Bytes()))
		if err != nil {
			return fmt.Errorf("pacman worker: signing %s: %w", path, err)
		}

		blobs[path+".sig"] = sig
	}

	return nil
}

// pacmanEntries returns the database entries of one architecture: the newest
// version of every package name that is built for it or for any architecture.
// When both exist at the same version the architecture-specific one is listed.
func pacmanEntries(pkgs []*models.PacmanPackage, lists map[string]string, arch string) []pacman.DBEntry {
	newest := make(map[string]*models.PacmanPackage)

	for _, p := range pkgs {
		if p.Arch != arch && p.Arch != pacman.ArchAny {
			continue
		}

		cur, ok := newest[p.Name]
		if !ok {
			newest[p.Name] = p
			continue
		}

		cmp := pacman.Vercmp(p.Version, cur.Version)
		if cmp > 0 || (cmp == 0 && cur.Arch == pacman.ArchAny && p.Arch != pacman.ArchAny) {
			newest[p.Name] = p
		}
	}

	names := make([]string, 0, len(newest))
	for name := range newest {
		names = append(names, name)
	}

	sort.Strings(names)

	entries := make([]pacman.DBEntry, 0, len(names))

	for _, name := range names {
		p := newest[name]

		var files []string
		if list := lists[p.ID]; list != "" {
			files = strings.Split(list, "\n")
		}

		entries = append(entries, pacman.DBEntry{
			Filename:     p.Filename,
			Name:         p.Name,
			Base:         p.Base,
			Version:      p.Version,
			Description:  p.Description,
			Groups:       p.Groups,
			CSize:        p.SizeBytes,
			ISize:        p.InstalledSize,
			MD5:          p.MD5,
			SHA256:       p.SHA256,
			PGPSig:       p.Signature,
			URL:          p.URL,
			Licenses:     p.Licenses,
			Arch:         p.Arch,
			BuildDate:    p.BuildDate,
			Packager:     p.Packager,
			Replaces:     p.Replaces,
			Conflicts:    p.Conflicts,
			Provides:     p.Provides,
			Depends:      p.Depends,
			OptDepends:   p.OptDepends,
			MakeDepends:  p.MakeDepends,
			CheckDepends: p.CheckDepends,
			Files:        files,
		})
	}

	return entries
}

// listPacmanMetadata returns path -> blob sha256 of the generated metadata rows.
func listPacmanMetadata(ctx context.Context, tx sq.Transaction, repoID string) (map[string]string, error) {
	return queryMetadataSums(ctx, tx, "pacman worker", "repo_metadata",
		`SELECT path, blob_sha256 FROM repository_metadata WHERE repository_id = $1 AND (path LIKE 'os/%' OR path = 'public.key')`,
		repoID,
	)
}

// publish makes blobs the repository's generated metadata: it stores the
// changed blobs, points the repository_metadata rows at them, drops the rows
// of paths that are no longer generated and reclaims the superseded blobs.
func (w *PacmanMetadataWorker) publish(ctx context.Context, repoID string, blobs map[string][]byte) error {
	tx, err := w.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("pacman worker: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	oldSHA, err := listPacmanMetadata(ctx, tx, repoID)
	if err != nil {
		return err
	}

	attrs := func(path, sum string) map[string]string {
		return map[string]string{"path": path, "format": "pacman", "repo_id": repoID, "sha256": sum}
	}

	newSHA := make(map[string]string, len(blobs))
	changed := make(map[string][]byte, len(blobs))

	for path, data := range blobs {
		checksum := fmt.Sprintf("%x", sha256.Sum256(data))
		newSHA[path] = checksum

		if oldSHA[path] == checksum {
			continue
		}

		changed[path] = data

		if _, err := w.blobStore.Put(ctx, bytes.NewReader(data), attrs(path, checksum)); err != nil {
			return fmt.Errorf("pacman worker: storing blob %s: %w", path, err)
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO repository_metadata (id, repository_id, path, blob_sha256, blob_size, last_generated)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, now())
			ON CONFLICT (repository_id, path) DO UPDATE
			SET blob_sha256 = $3, blob_size = $4, last_generated = now()
		`, repoID, path, checksum, int64(len(data)))
		if err != nil {
			return fmt.Errorf("pacman worker: updating repo_metadata for %s: %w", path, err)
		}
	}

	stalePaths, reclaim := supersededMetadata(oldSHA, newSHA)

	for _, path := range stalePaths {
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM repository_metadata WHERE repository_id = $1 AND path = $2`,
			repoID,
			path,
		); err != nil {
			return fmt.Errorf("pacman worker: deleting stale repo_metadata %s: %w", path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pacman worker: commit: %w", err)
	}

	w.blobs().ensure(ctx, repoID, changed, newSHA, attrs)
	w.blobs().reclaim(ctx, reclaim)

	w.log.Infow("pacman metadata regeneration complete", "repo_id", repoID, "documents", len(blobs))

	return nil
}
