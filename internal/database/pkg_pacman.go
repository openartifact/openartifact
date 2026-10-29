// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// PacmanStore owns the Arch Linux pacman packages.
//
// A pacman package lives on the generic package tables (packages P, package_versions V,
// package_files F) plus the side table pacman_files. Grain 1:1: one package file of
// (name, version, arch) is one P (shared by name), one V and one F. The mapping:
//
//	P  name, name_key = pkgname; attrs = {}
//	V  version        = [epoch:]pkgver-pkgrel as stored
//	   version_key    = version|arch (the legacy live key is name, version, arch; repo_arch
//	                    is not part of it)
//	   metadata       = {base, repo_arch, description, url, packager, licenses[],
//	                    package_groups[], replaces[], conflicts[], provides[], depends[],
//	                    optdepends[], makedepends[], checkdepends[]}; all keys written,
//	                    the arrays keep their order (the index emits them in that order)
//	F  path, filename = the published file name (it renders name-version-arch)
//	   arch           = the package's own arch ('any' or a CPU), not repo_arch
//	   blob_id        = the package blob; size_bytes = NULL; part = ''; checksums = {}
//	   attrs          = {build_time, installed_size, md5}: build_time (the legacy
//	                    build_date) and installed_size only when not zero, md5 always. md5
//	                    is not in checksums: checksum search never matched pacman
//	pacman_files (id = F.id): file_list (NULL = not read from the package yet: it stays NULL
//	   and SetPacmanFileList only writes where it is NULL), signature (base64, '' unsigned),
//	   signature_source ('' | 'uploader' | 'server'), signature_key (fingerprint, '')
type PacmanStore struct{ *core }

// pacmanMeta is package_versions.metadata of a pacman package.
type pacmanMeta struct {
	Base          string   `json:"base"`
	RepoArch      string   `json:"repo_arch"`
	Description   string   `json:"description"`
	URL           string   `json:"url"`
	Packager      string   `json:"packager"`
	Licenses      []string `json:"licenses"`
	PackageGroups []string `json:"package_groups"`
	Replaces      []string `json:"replaces"`
	Conflicts     []string `json:"conflicts"`
	Provides      []string `json:"provides"`
	Depends       []string `json:"depends"`
	OptDepends    []string `json:"optdepends"`
	MakeDepends   []string `json:"makedepends"`
	CheckDepends  []string `json:"checkdepends"`
}

// pacmanMediaType maps the compression of a package file to the media type of
// its blob.
func pacmanMediaType(filename string) string {
	switch {
	case strings.HasSuffix(filename, ".zst"):
		return "application/zstd"
	case strings.HasSuffix(filename, ".xz"):
		return "application/x-xz"
	case strings.HasSuffix(filename, ".gz"):
		return "application/gzip"
	default:
		return ""
	}
}

// pacmanRow lists the sqlc row types pacmanFromRow converts; they share the columns of ListPacmanPackagesRow.
type pacmanRow interface {
	dbsqlc.ListPacmanPackagesRow | dbsqlc.GetPacmanPackageByFilenameRow
}

func pacmanFromRow[R pacmanRow](in R) (*models.PacmanPackage, error) {
	row := dbsqlc.ListPacmanPackagesRow(in)

	var meta pacmanMeta
	if err := json.Unmarshal(row.Metadata, &meta); err != nil {
		return nil, fmt.Errorf("decode pacman metadata of %s: %w", row.ID, err)
	}

	return &models.PacmanPackage{
		ID:              row.ID,
		RepositoryID:    row.RepositoryID,
		Name:            row.Name,
		Base:            meta.Base,
		Version:         row.Version,
		Arch:            row.Arch,
		RepoArch:        meta.RepoArch,
		Filename:        row.Filename,
		BlobKey:         row.BlobKey,
		SizeBytes:       row.SizeBytes,
		SHA256:          row.Sha256,
		MD5:             row.Md5,
		Description:     meta.Description,
		URL:             meta.URL,
		Packager:        meta.Packager,
		BuildDate:       row.BuildDate,
		InstalledSize:   row.InstalledSize,
		Licenses:        meta.Licenses,
		Groups:          meta.PackageGroups,
		Replaces:        meta.Replaces,
		Conflicts:       meta.Conflicts,
		Provides:        meta.Provides,
		Depends:         meta.Depends,
		OptDepends:      meta.OptDepends,
		MakeDepends:     meta.MakeDepends,
		CheckDepends:    meta.CheckDepends,
		Signature:       row.Signature,
		SignatureSource: row.SignatureSource,
		SignatureKey:    row.SignatureKey,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}, nil
}

// CreatePacmanPackage stores one package file. A live row with the same file
// name, or the same name, version and architecture, is ErrDuplicate.
func (p *PacmanStore) CreatePacmanPackage(ctx context.Context, pkg *models.PacmanPackage) error {
	blobID, err := p.upsertBlobWithMediaType(ctx, pkg.SHA256, pkg.BlobKey, pacmanMediaType(pkg.Filename), pkg.SizeBytes)
	if err != nil {
		return fmt.Errorf("failed to upsert blob for pacman package: %w", err)
	}

	attrs := map[string]any{"md5": pkg.MD5}
	putNonZero(attrs, "build_time", pkg.BuildDate)
	putNonZero(attrs, "installed_size", pkg.InstalledSize)

	written, err := p.writeOSPackage(ctx, &PackageWrite{
		RepoID:  pkg.RepositoryID,
		Format:  "pacman",
		Name:    pkg.Name,
		NameKey: pkg.Name,
		Version: pkg.Version, VersionKey: pkg.Version + "|" + pkg.Arch,
		VersionMeta: pacmanMeta{
			Base:          pkg.Base,
			RepoArch:      pkg.RepoArch,
			Description:   pkg.Description,
			URL:           pkg.URL,
			Packager:      pkg.Packager,
			Licenses:      nonNilStrings(pkg.Licenses),
			PackageGroups: nonNilStrings(pkg.Groups),
			Replaces:      nonNilStrings(pkg.Replaces),
			Conflicts:     nonNilStrings(pkg.Conflicts),
			Provides:      nonNilStrings(pkg.Provides),
			Depends:       nonNilStrings(pkg.Depends),
			OptDepends:    nonNilStrings(pkg.OptDepends),
			MakeDepends:   nonNilStrings(pkg.MakeDepends),
			CheckDepends:  nonNilStrings(pkg.CheckDepends),
		},
		Exclusive: true,
		CreatedAt: pkg.CreatedAt,
		Files: []FileWrite{{
			Path:     pkg.Filename,
			Filename: pkg.Filename,
			Arch:     pkg.Arch,
			Attrs:    attrs,
		}},
	}, []*string{&blobID}, func(q *dbsqlc.Queries, files []FileWritten) error {
		if err := q.InsertPacmanFile(ctx, dbsqlc.InsertPacmanFileParams{
			ID:              files[0].ID,
			FileList:        pkg.FileList,
			Signature:       pkg.Signature,
			SignatureSource: pkg.SignatureSource,
			SignatureKey:    pkg.SignatureKey,
		}); err != nil {
			return fmt.Errorf("insert pacman file: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	pkg.ID, pkg.CreatedAt, pkg.UpdatedAt = written.Files[0].ID, written.Files[0].CreatedAt, written.Files[0].UpdatedAt

	return nil
}

// GetPacmanPackageByFilename returns the live package published under the file
// name, or (nil, nil).
func (p *PacmanStore) GetPacmanPackageByFilename(ctx context.Context, repoID, filename string) (*models.PacmanPackage, error) {
	row, err := p.q.GetPacmanPackageByFilename(ctx, dbsqlc.GetPacmanPackageByFilenameParams{RepositoryID: repoID, Filename: filename})

	return mapRowErr(row, err, "get pacman package", pacmanFromRow)
}

// ListPacmanPackages returns every live package of the repository, without file lists.
func (p *PacmanStore) ListPacmanPackages(ctx context.Context, repoID string) ([]*models.PacmanPackage, error) {
	rows, err := p.q.ListPacmanPackages(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pacman packages: %w", err)
	}

	pkgs := make([]*models.PacmanPackage, len(rows))

	for i, row := range rows {
		if pkgs[i], err = pacmanFromRow(row); err != nil {
			return nil, err
		}
	}

	return pkgs, nil
}

// ListPacmanFileLists returns the file lists of the live packages that have one.
func (p *PacmanStore) ListPacmanFileLists(ctx context.Context, repoID string) (map[string]string, error) {
	rows, err := p.q.ListPacmanFileLists(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pacman file lists: %w", err)
	}

	lists := make(map[string]string, len(rows))

	for _, row := range rows {
		if row.FileList != nil {
			lists[row.ID] = *row.FileList
		}
	}

	return lists, nil
}

// ListPacmanPackagesWithoutFileList returns the live packages that still need
// their file list read from the blob.
func (p *PacmanStore) ListPacmanPackagesWithoutFileList(ctx context.Context, repoID string) ([]*models.PacmanPackage, error) {
	rows, err := p.q.ListPacmanPackagesWithoutFileList(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list pacman packages without file list: %w", err)
	}

	pkgs := make([]*models.PacmanPackage, len(rows))
	for i, row := range rows {
		pkgs[i] = &models.PacmanPackage{ID: row.ID, RepositoryID: repoID, BlobKey: row.BlobKey, Filename: row.Filename}
	}

	return pkgs, nil
}

// SetPacmanFileList records the file list of a package that has none.
func (p *PacmanStore) SetPacmanFileList(ctx context.Context, repoID, id, fileList string) error {
	if err := p.q.SetPacmanFileList(ctx, dbsqlc.SetPacmanFileListParams{ID: id, RepositoryID: repoID, FileList: &fileList}); err != nil {
		return fmt.Errorf("failed to set pacman file list: %w", err)
	}

	return nil
}

// SetPacmanSignature records the signature of a package file.
func (p *PacmanStore) SetPacmanSignature(ctx context.Context, repoID, id, signature, source, key string) error {
	err := p.q.SetPacmanSignature(ctx, dbsqlc.SetPacmanSignatureParams{
		ID: id, RepositoryID: repoID, Signature: signature, SignatureSource: source, SignatureKey: key,
	})
	if err != nil {
		return fmt.Errorf("failed to set pacman signature: %w", err)
	}

	return nil
}

// DeletePacmanPackage removes a live package row (and its version and package
// when nothing else holds them).
func (p *PacmanStore) DeletePacmanPackage(ctx context.Context, repoID, id string) error {
	if _, err := p.deleteLiveFiles(ctx, repoID, nil, []string{id}); err != nil {
		return fmt.Errorf("failed to delete pacman package: %w", err)
	}

	return nil
}
