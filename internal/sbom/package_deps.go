// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package sbom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/pkgdeps"
)

// ErrUnsafeArtifact means the stored package file is too large to read safely
// or holds several candidates for its manifest; no SBOM is built from it.
var ErrUnsafeArtifact = pkgdeps.ErrUnsafe

// packageReadTimeout bounds reading the dependencies out of one stored package.
const packageReadTimeout = 2 * time.Minute

// packageFile is the stored file an SBOM reads dependencies from.
type packageFile struct {
	Format   string
	Filename string
	SHA256   string
}

// loadSBOM is load plus what only SBOM documents need: the artifact is
// identified (an id that is a package-listing row id gives the subject its real
// name and version) and the dependencies its stored package file declares are
// merged with the ones of its "dependencies" property.
func (g *Generator) loadSBOM(ctx context.Context, repoID string, in *Input) error {
	var file *packageFile

	if g.db != nil {
		var err error

		if file, err = g.identify(ctx, repoID, in); err != nil {
			return err
		}
	}

	if err := g.load(ctx, repoID, in); err != nil {
		return err
	}

	if file == nil || g.blobs == nil {
		return nil
	}

	return g.loadPackageDeps(ctx, repoID, in, file)
}

// identify finds the stored package file of the artifact. artifact_id is
// either a package-listing row id or "name@version"; a row id also replaces
// the subject's name and version, which are otherwise derived from the id.
func (g *Generator) identify(ctx context.Context, repoID string, in *Input) (*packageFile, error) {
	if uuid.Validate(in.ArtifactID) == nil {
		blob, err := g.db.GetRepoComponentBlob(ctx, repoID, in.ArtifactID)
		if err != nil {
			return nil, fmt.Errorf("resolve artifact: %w", err)
		}

		if blob == nil {
			return nil, nil //nolint:nilnil // not a package row: properties only
		}

		if blob.Name != "" {
			in.Name, in.Version = blob.Name, blob.Version
			if in.Version == "" {
				in.Version = "unknown"
			}

			in.PURL = generatePURL(in.Format, in.Name, in.Version)
		}

		in.SHA256 = strings.ToLower(blob.SHA256)

		return &packageFile{Format: blob.Format, Filename: blob.Filename, SHA256: blob.SHA256}, nil
	}

	if in.Version == "unknown" {
		return nil, nil //nolint:nilnil // no version, no unambiguous package
	}

	searcher, ok := g.db.(database.ComponentSearcher)
	if !ok {
		return nil, nil //nolint:nilnil // the store cannot list packages
	}

	comps, _, err := searcher.SearchComponents(ctx, repoID, database.ComponentListOptions{
		Name: in.Name, Query: in.Version, Page: 1, Limit: maxHashLookup,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve artifact: %w", err)
	}

	return pickPackageFile(comps, in.Version), nil
}

// pickPackageFile chooses the file that represents the package among the rows
// of a name and version: the descriptor that carries the manifest (a .pom beside
// its jars), else any file a manifest can be read from, the first by file name.
func pickPackageFile(comps []*models.Component, version string) *packageFile {
	var rows []*models.Component

	for _, c := range comps {
		if c.Version == version && c.SHA256 != "" {
			rows = append(rows, c)
		}
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Filename < rows[j].Filename })

	for _, eligible := range []bool{true, false} {
		for _, c := range rows {
			ok := pkgdeps.Supported(c.Format, c.Filename)
			if eligible {
				ok = pkgdeps.Eligible(c.Format, c.Filename)
			}

			if ok {
				return &packageFile{Format: c.Format, Filename: c.Filename, SHA256: c.SHA256}
			}
		}
	}

	return nil
}

// loadPackageDeps reads the package file and appends the dependencies it
// declares to those the properties listed. A file that cannot be read (damaged,
// not the archive its name says) leaves the properties as the only source; one
// that is unsafe to read fails the SBOM.
func (g *Generator) loadPackageDeps(ctx context.Context, repoID string, in *Input, file *packageFile) error {
	if !pkgdeps.Supported(file.Format, file.Filename) {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, packageReadTimeout)
	defer cancel()

	src := pkgdeps.Source{
		Format:   file.Format,
		Filename: file.Filename,
		Open:     func() (io.ReadCloser, error) { return g.blobs.Get(ctx, file.SHA256) },
	}

	if file.Format == "conan" {
		src.Sibling = func(name string) (io.ReadCloser, error) { return g.conanSibling(ctx, repoID, file.Filename, name) }
	}

	res, err := pkgdeps.Read(ctx, src)

	switch {
	case err == nil:
	case errors.Is(err, pkgdeps.ErrUnsafe):
		return fmt.Errorf("read package dependencies: %w", err)
	case errors.Is(err, manifest.ErrNotFound), errors.Is(err, pkgdeps.ErrUnsupported):
		return nil
	default:
		if g.log != nil {
			g.log.Warnw("sbom: package file unreadable, using properties only",
				"repository_id", repoID, "artifact_id", in.ArtifactID, "format", file.Format, "error", err)
		}

		return nil
	}

	in.addPackageDeps(res)

	return nil
}

// conanSibling opens a recipe file stored next to the conanmanifest.txt the
// package listing points at; the reader is nil when the recipe has no such file.
func (g *Generator) conanSibling(ctx context.Context, repoID, reference, file string) (io.ReadCloser, error) {
	key, ok := models.ParseConanReference(reference)
	if !ok {
		return nil, nil //nolint:nilnil // no such file
	}

	key.Filename = file

	row, err := g.db.GetConanPackage(ctx, repoID, key)
	if err != nil {
		return nil, fmt.Errorf("find conan recipe file: %w", err)
	}

	if row == nil || row.BlobKey == "" {
		return nil, nil //nolint:nilnil // no such file
	}

	return g.blobs.Get(ctx, row.BlobKey)
}

// addPackageDeps appends the dependencies read from the package file; an entry
// the properties already list (same purl) keeps its richer property data.
func (in *Input) addPackageDeps(res pkgdeps.Result) {
	seen := make(map[string]struct{}, len(in.Deps))
	for _, d := range in.Deps {
		seen[d.PURL] = struct{}{}
	}

	for _, fd := range res.Deps {
		name := strings.TrimSpace(spdxSafe(fd.Name))
		if name == "" {
			continue
		}

		version := strings.TrimSpace(spdxSafe(fd.Version))
		purl := generatePURLNS(in.Format, res.Namespace, name, version)

		if _, dup := seen[purl]; dup {
			continue
		}

		seen[purl] = struct{}{}

		dep := Dep{Name: name, Version: version, PURL: purl, Range: strings.TrimSpace(spdxSafe(fd.Range))}
		if fd.License != "" {
			dep.Licenses = classifyLicenses([]string{spdxSafe(fd.License)})
		}

		in.Deps = append(in.Deps, dep)
	}
}
