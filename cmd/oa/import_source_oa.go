// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
)

// ─── OpenArtifact (0.14) ──────────────────────────────────────────────────────
//
// The source is a running OpenArtifact instance that is read through its public
// API: the repository list and the package listing of REST, and the registry,
// index and file endpoints of the formats where one listing row is not one
// file (docker, conan, deb placement, nix, terraform). It only reads. Every
// hosted repository is imported into a hosted repository of the destination by
// the format's own upload, so the destination (a fresh release 0.15) checks and
// indexes what it stores exactly as it does for a client's publish.
//
// A row of the package listing becomes one asset whose path is what the
// format's importer expects (see assetOf). The creation time of the row is the
// asset's, which the importer sends as the import time header.

type openartifactSource struct {
	http *sourceHTTP
	// props asks Details to read the artifact properties.
	props bool
	// debDists are the Debian distributions to look for the placement of a
	// package in; the API does not list them.
	debDists []string
}

const (
	oaPageSize = 100
	// oaExtraID is the Extra key that carries the id of the listing row, which
	// the properties are read by.
	oaExtraID = "oa.id"
	// oaExtraNarInfo marks a nix asset whose narinfo Details has to read.
	oaExtraNarInfo = "oa.narinfo"
)

// oaRepository is the part of a repository of the source the importer reads.
type oaRepository struct {
	Name     string `json:"name"`
	Format   string `json:"format"`
	RepoType string `json:"repo_type"`
}

// oaPackage is one row of the package listing.
type oaPackage struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Arch      string `json:"arch"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Filename  string `json:"filename"`
	CreatedAt string `json:"created_at"`
}

func (s *openartifactSource) apiPath(elem ...string) string {
	var b strings.Builder

	b.WriteString("/api/v1")

	for _, e := range elem {
		b.WriteString("/" + e)
	}

	return s.http.resolve(b.String())
}

// repositories lists the repositories of the source.
func (s *openartifactSource) repositories(ctx context.Context) ([]oaRepository, error) {
	var out []oaRepository

	for page := 1; ; page++ {
		var resp struct {
			Total        int            `json:"total"`
			Repositories []oaRepository `json:"repositories"`
		}

		u := s.apiPath("repositories") + "?page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(oaPageSize)
		if err := s.http.getJSON(ctx, u, &resp); err != nil {
			return nil, fmt.Errorf("listing repositories: %w", err)
		}

		out = append(out, resp.Repositories...)

		if len(resp.Repositories) == 0 || len(out) >= resp.Total {
			return out, nil
		}
	}
}

// hostedRepositories names the hosted repositories of a format the importer
// can copy, restricted to formats when it is not empty. The others (proxy and
// group repositories hold no data of their own) are returned separately.
func (s *openartifactSource) hostedRepositories(ctx context.Context, formats []string) ([]oaRepository, []oaRepository, error) {
	all, err := s.repositories(ctx)
	if err != nil {
		return nil, nil, err
	}

	var hosted, skipped []oaRepository

	for _, r := range all {
		_, known := formatDefs[importFormat(r.Format)]

		switch {
		case r.RepoType != "hosted" || !known:
			skipped = append(skipped, r)
		case len(formats) > 0 && !slices.Contains(formats, r.Format):
			continue
		default:
			hosted = append(hosted, r)
		}
	}

	return hosted, skipped, nil
}

func (s *openartifactSource) Repository(ctx context.Context, name string) (sourceRepo, error) {
	var r oaRepository

	switch err := s.http.getJSON(ctx, s.apiPath("repository", url.PathEscape(name)), &r); {
	case errors.Is(err, errNotFound):
		return sourceRepo{}, fmt.Errorf("%w: %q", errSourceRepoNotFound, name)
	case err != nil:
		return sourceRepo{}, err
	}

	if r.RepoType != "hosted" {
		return sourceRepo{}, fmt.Errorf("%w: %q is a %s repository", errSourceNotHosted, name, r.RepoType)
	}

	f, ok := formatDefs[importFormat(r.Format)]
	if !ok {
		return sourceRepo{}, fmt.Errorf("%w: %q has format %q", errSourceFormat, name, r.Format)
	}

	return sourceRepo{Name: name, Format: f.format}, nil
}

// listPackages reads the whole package listing of a repository, in the
// listing's stable name order.
func (s *openartifactSource) listPackages(ctx context.Context, repo sourceRepo) ([]oaPackage, error) {
	var out []oaPackage

	for page := 1; ; page++ {
		var resp struct {
			Total    int         `json:"total"`
			Packages []oaPackage `json:"packages"`
		}

		u := s.apiPath("repositories", url.PathEscape(repo.Name), "packages") +
			"?format=" + url.QueryEscape(string(repo.Format)) + "&sort=name&page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(oaPageSize)
		if err := s.http.getJSON(ctx, u, &resp); err != nil {
			return nil, fmt.Errorf("listing packages of %q: %w", repo.Name, err)
		}

		out = append(out, resp.Packages...)

		if len(resp.Packages) == 0 || len(out) >= resp.Total {
			return out, nil
		}
	}
}

func (s *openartifactSource) Assets(ctx context.Context, repo sourceRepo) ([]sourceAsset, error) {
	rows, err := s.listPackages(ctx, repo)
	if err != nil {
		return nil, err
	}

	var assets []sourceAsset

	for _, row := range rows {
		if repo.Format == formatConan {
			files, err := s.conanAssets(ctx, repo, row)
			if err != nil {
				return nil, err
			}

			assets = append(assets, files...)

			continue
		}

		assets = append(assets, s.assetOf(repo, row))
	}

	switch repo.Format {
	case formatDeb:
		err = s.placeDebs(ctx, repo, assets)
	case formatTerraform:
		err = s.terraformProtocols(ctx, repo, assets)
	case formatDocker:
		assets, err = s.dockerReferrers(ctx, repo, assets)
	}

	if err != nil {
		return nil, err
	}

	return assets, nil
}

// assetOf turns a listing row into the asset the format's importer expects.
// The path is what the importer parses (the layout of the format's own upload
// path), assembled from the row because the listing does not carry it.
func (s *openartifactSource) assetOf(repo sourceRepo, row oaPackage) sourceAsset {
	a := sourceAsset{
		Repo:    repo.Name,
		Path:    oaAssetPath(repo, row),
		URL:     s.apiPath("repositories", url.PathEscape(repo.Name), "packages", url.PathEscape(row.ID), "download"),
		Size:    row.Size,
		SHA256:  strings.ToLower(row.SHA256),
		Created: parseSourceTime(row.CreatedAt),
		Extra:   map[string]string{oaExtraID: row.ID},
	}

	switch repo.Format {
	case formatNix:
		a.Extra[oaExtraNarInfo] = "1"
	case formatNuGet, formatComposer:
		// The package's own coordinates: the version of a composer dist is
		// usually not in its composer.json.
		a.Component = sourceComponent{Name: row.Name, Version: row.Version}
	}

	return a
}

// oaAssetPath is the importer-facing path of a listing row.
func oaAssetPath(repo sourceRepo, row oaPackage) string {
	switch repo.Format {
	case formatNPM:
		return row.Name + "/-/" + row.Filename
	case formatGo:
		return row.Name + "/@v/" + row.Version + path.Ext(row.Filename)
	case formatCargo:
		return "crates/" + row.Name + "/" + row.Filename
	case formatConda:
		return row.Arch + "/" + row.Filename
	case formatIvy:
		org, module, _ := strings.Cut(row.Name, ":")

		return org + "/" + module + "/" + row.Version + "/" + ivyArtifactName(row.Filename)
	case formatSBT:
		group, artifact, _ := strings.Cut(row.Name, ":")

		return group + "/" + artifact + "/" + row.Version + "/" + row.Filename
	case formatAPK:
		return path.Dir(row.Name) + "/" + row.Arch + "/" + row.Filename
	case formatTerraform:
		if strings.HasPrefix(row.Name, "providers/") {
			osName, goArch, _ := strings.Cut(row.Arch, "_")

			return row.Name + "/" + row.Version + "/" + osName + "/" + goArch
		}

		return row.Name + "/" + row.Version
	case formatDocker:
		return dockerSourcePath(repo.Name, row.Name, row.Version)
	}

	return strings.TrimPrefix(row.Filename, "/")
}

// ivyArtifactName is the file name of an ivy listing row. A server before the
// generic package tables listed the stored name followed by its extension again
// ("lib-1.0.jar.jar"); that repeat is dropped, any other name is the stored one.
func ivyArtifactName(listed string) string {
	ext := path.Ext(listed)
	if ext == "" {
		return listed
	}

	if name, ok := strings.CutSuffix(listed, ext); ok && path.Ext(name) == ext {
		return name
	}

	return listed
}

// dockerSourcePath is the source path of a tag: the registry address of its
// manifest. The listing names an image "<repository>/<image>", or the
// repository itself for the tags pushed without an image name.
func dockerSourcePath(repo, name, tag string) string {
	image := strings.TrimPrefix(strings.TrimPrefix(name, repo), "/")
	if image == "" {
		return dockerRootManifests + tag
	}

	return "v2/" + image + "/manifests/" + tag
}

// ─── Details and content ──────────────────────────────────────────────────────

// Details reads what the listing does not carry: the properties of the artifact
// and, for a nix store path, its narinfo.
func (s *openartifactSource) Details(ctx context.Context, a *sourceAsset) error {
	id := a.Extra[oaExtraID]

	if s.props && id != "" {
		var props map[string]string

		u := s.apiPath("repositories", url.PathEscape(a.Repo), "artifacts", url.PathEscape(id), "properties")

		switch err := s.http.getJSON(ctx, u, &props); {
		case errors.Is(err, errNotFound):
			// a source without properties for this row
		case err != nil:
			return fmt.Errorf("properties of %q: %w", a.Path, err)
		default:
			merged := make(map[string][]string, len(a.Props)+len(props))
			maps.Copy(merged, a.Props)

			for k, v := range props {
				merged[k] = []string{v}
			}

			a.Props = merged
		}
	}

	if a.Extra[oaExtraNarInfo] != "" && a.Extra[nixNarInfoKey] == "" {
		body, err := s.http.open(ctx, s.http.resolve("/repos/"+url.PathEscape(a.Repo)+"/"+url.PathEscape(a.Path)))
		if err != nil {
			return fmt.Errorf("narinfo of %q: %w", a.Path, err)
		}

		defer func() { _ = body.Close() }()

		text, err := io.ReadAll(io.LimitReader(body, 1<<20))
		if err != nil {
			return fmt.Errorf("narinfo of %q: %w", a.Path, err)
		}

		a.Extra = maps.Clone(a.Extra)
		a.Extra[nixNarInfoKey] = string(text)
	}

	return nil
}

func (s *openartifactSource) Open(ctx context.Context, a sourceAsset) (io.ReadCloser, error) {
	return s.http.open(ctx, a.URL)
}

// registry serves the docker registry API of a hosted repository.
func (s *openartifactSource) registry(repo string) ociRegistry {
	return ociRegistry{http: s.http, base: s.http.resolve("/v2/" + url.PathEscape(repo))}
}

// cargoIndexURLs is where the sparse index of the repository serves the index
// file of a crate: it names which versions are yanked.
func (s *openartifactSource) cargoIndexURLs(a sourceAsset, rel string) []string {
	return []string{s.http.resolve("/repos/" + url.PathEscape(a.Repo) + "/index/" + escapePath(rel))}
}

// ─── Debian placement ─────────────────────────────────────────────────────────

// placeDebs sets the distribution and component of every deb of the repository.
// The listing does not say where a package is published; the Packages indexes
// of the distributions in debDists do (Filename: pool/<component>/...). A deb
// that no index lists keeps no placement and takes the --deb-distribution and
// --deb-component fallbacks. A deb published in several distributions is
// listed once, under the first distribution that has it.
func (s *openartifactSource) placeDebs(ctx context.Context, repo sourceRepo, assets []sourceAsset) error {
	place := map[string][2]string{}

	for _, dist := range s.debDists {
		comps, archs, err := s.debRelease(ctx, repo.Name, dist)
		if err != nil {
			return err
		}

		for _, comp := range comps {
			for _, arch := range archs {
				u := s.http.resolve("/repos/" + url.PathEscape(repo.Name) + "/dists/" + url.PathEscape(dist) + "/" +
					url.PathEscape(comp) + "/binary-" + url.PathEscape(arch) + "/Packages")

				if err := s.debPackages(ctx, u, func(file string) {
					if _, seen := place[file]; !seen {
						place[file] = [2]string{dist, comp}
					}
				}); err != nil {
					return err
				}
			}
		}
	}

	for i := range assets {
		if p, ok := place[path.Base(assets[i].Path)]; ok {
			assets[i].Props = map[string][]string{"deb.distribution": {p[0]}, "deb.component": {p[1]}}
		}
	}

	return nil
}

// debRelease reads the components and architectures a distribution declares.
// A distribution the repository does not have yields none.
func (s *openartifactSource) debRelease(ctx context.Context, repo, dist string) ([]string, []string, error) {
	var comps, archs []string

	body, err := s.openOptional(ctx, s.http.resolve("/repos/"+url.PathEscape(repo)+"/dists/"+url.PathEscape(dist)+"/Release"))
	if err != nil || body == nil {
		return nil, nil, err
	}

	defer func() { _ = body.Close() }()

	sc := bufio.NewScanner(body)
	for sc.Scan() {
		switch key, val, _ := strings.Cut(sc.Text(), ":"); key {
		case "Components":
			comps = strings.Fields(val)
		case "Architectures":
			archs = strings.Fields(val)
		}
	}

	return comps, archs, sc.Err()
}

// debPackages calls fn with the base name of every Filename of a Packages index.
func (s *openartifactSource) debPackages(ctx context.Context, u string, fn func(file string)) error {
	body, err := s.openOptional(ctx, u)
	if err != nil || body == nil {
		return err
	}

	defer func() { _ = body.Close() }()

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "Filename:"); ok {
			fn(path.Base(strings.TrimSpace(v)))
		}
	}

	return sc.Err()
}

// openOptional opens u; a 404 yields no body and no error.
func (s *openartifactSource) openOptional(ctx context.Context, u string) (io.ReadCloser, error) {
	var body io.ReadCloser

	err := s.http.retry.do(ctx, "GET "+redactURL(u), func(int) error {
		var err error

		body, err = s.http.open(ctx, u)

		return err
	})

	var se *httpStatusError
	if errors.As(err, &se) && se.status == http.StatusNotFound {
		return nil, nil //nolint:nilnil // absence is not an error here
	}

	return body, err
}

// ─── Terraform ────────────────────────────────────────────────────────────────

// terraformProtocols reads the plugin protocols each provider version declares
// from the registry's version list, so they are published as they were.
func (s *openartifactSource) terraformProtocols(ctx context.Context, repo sourceRepo, assets []sourceAsset) error {
	byType := map[string]map[string]string{}

	for i := range assets {
		c, ok := parseTerraformPath(assets[i].Path)
		if !ok || c.module {
			continue
		}

		versions, seen := byType[c.name]
		if !seen {
			var resp struct {
				Versions []struct {
					Version   string   `json:"version"`
					Protocols []string `json:"protocols"`
				} `json:"versions"`
			}

			err := s.http.getJSON(ctx, s.http.resolve("/repos/"+url.PathEscape(repo.Name)+"/"+url.PathEscape(c.name)+"/versions"), &resp)
			if err != nil && !errors.Is(err, errNotFound) {
				return fmt.Errorf("provider versions of %q: %w", c.name, err)
			}

			versions = map[string]string{}
			for _, v := range resp.Versions {
				versions[v.Version] = strings.Join(v.Protocols, ",")
			}

			byType[c.name] = versions
		}

		if p := versions[c.version]; p != "" {
			assets[i].Extra[terraformProtocolsKey] = p
		}
	}

	return nil
}

// ─── Docker referrers ─────────────────────────────────────────────────────────

// dockerReferrers adds the manifests attached to a tag's image through the OCI
// 1.1 subject field (signatures, SBOMs, attestations) as assets named by their
// digest: they have no tag, so the package listing does not show them. The
// referrers of a referrer are followed. Other manifests that no tag leads to
// cannot be told from the API and are not imported.
func (s *openartifactSource) dockerReferrers(ctx context.Context, repo sourceRepo, tags []sourceAsset) ([]sourceAsset, error) {
	reg := s.registry(repo.Name)
	known := map[string]bool{}

	type item struct{ image, digest string }

	var queue []item

	for _, a := range tags {
		id, ok := dockerParsePath(a.Path)
		if !ok || a.SHA256 == "" {
			continue
		}

		d := ociDigestPrefix + a.SHA256
		known[id.Image+"@"+d] = true

		queue = append(queue, item{id.Image, d})
	}

	out := tags

	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]

		var idx struct {
			Manifests []ociDescriptor `json:"manifests"`
		}

		switch err := s.http.getJSON(ctx, reg.url(it.image, "referrers", it.digest), &idx); {
		case errors.Is(err, errNotFound):
			continue
		case err != nil:
			return nil, fmt.Errorf("referrers of %s@%s: %w", it.image, it.digest, err)
		}

		for _, m := range idx.Manifests {
			key := it.image + "@" + m.Digest
			if known[key] || !ociValidDigest(m.Digest) {
				continue
			}

			known[key] = true

			p := dockerRootManifests + m.Digest
			if it.image != "" {
				p = "v2/" + it.image + "/manifests/" + m.Digest
			}

			out = append(out, sourceAsset{
				Repo: repo.Name, Path: p, Size: m.Size, SHA256: strings.TrimPrefix(m.Digest, ociDigestPrefix),
				Extra: map[string]string{},
			})

			queue = append(queue, item{it.image, m.Digest})
		}
	}

	return out, nil
}

// ─── Conan ────────────────────────────────────────────────────────────────────

// conanAssets expands a recipe revision (the listing's row) into the files of
// the recipe and of every package revision of it, which the Conan 2 API lists.
// Each file is addressed as the importer's Conan 2 layout
// <user>/<name>/<version>/<channel>/<rrev>/export/<file> and
// <user>/<name>/<version>/<channel>/<rrev>/package/<pid>/<prev>/<file>.
func (s *openartifactSource) conanAssets(ctx context.Context, repo sourceRepo, row oaPackage) ([]sourceAsset, error) {
	// "<name>/<version>@<user>/<channel>#<rrev>"
	ref, rrev, _ := strings.Cut(row.Filename, "#")
	_, userChannel, _ := strings.Cut(ref, "@")

	user, channel, ok := strings.Cut(userChannel, "/")
	if !ok || rrev == "" || user == "" || channel == "" {
		return nil, fmt.Errorf("%w: conan reference %q of %q", errSourceFormat, row.Filename, repo.Name)
	}

	created := parseSourceTime(row.CreatedAt)
	recipe := "/repos/" + url.PathEscape(repo.Name) + "/v2/conans/" + url.PathEscape(row.Name) + "/" + url.PathEscape(row.Version) +
		"/" + url.PathEscape(user) + "/" + url.PathEscape(channel) + "/revisions/" + url.PathEscape(rrev)
	prefix := user + "/" + row.Name + "/" + row.Version + "/" + channel + "/" + rrev

	var out []sourceAsset

	files, err := s.conanFiles(ctx, recipe)
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		a := sourceAsset{
			Repo: repo.Name, Path: prefix + "/export/" + f.name, URL: s.http.resolve(recipe + "/files/" + escapePath(f.name)),
			Size: f.size, Created: created, Extra: map[string]string{},
		}

		if f.name == conanManifest {
			// The row is the manifest: its digest, id (properties) and time.
			a.SHA256, a.Extra[oaExtraID] = strings.ToLower(row.SHA256), row.ID
		}

		out = append(out, a)
	}

	var pids map[string]json.RawMessage

	switch err := s.http.getJSON(ctx, s.http.resolve(recipe+"/search?list_only=true"), &pids); {
	case errors.Is(err, errNotFound):
		return out, nil
	case err != nil:
		return nil, fmt.Errorf("packages of %q: %w", row.Filename, err)
	}

	for _, pid := range slices.Sorted(maps.Keys(pids)) {
		pkg := recipe + "/packages/" + url.PathEscape(pid)

		var revs struct {
			Revisions []struct {
				Revision string `json:"revision"`
				Time     string `json:"time"`
			} `json:"revisions"`
		}

		switch err := s.http.getJSON(ctx, s.http.resolve(pkg+"/revisions"), &revs); {
		case errors.Is(err, errNotFound):
			continue
		case err != nil:
			return nil, fmt.Errorf("package revisions of %q: %w", row.Filename, err)
		}

		for _, rev := range revs.Revisions {
			base := pkg + "/revisions/" + url.PathEscape(rev.Revision)

			pfiles, err := s.conanFiles(ctx, base)
			if err != nil {
				return nil, err
			}

			when := parseSourceTime(rev.Time)
			if when.IsZero() {
				when = created
			}

			for _, f := range pfiles {
				out = append(out, sourceAsset{
					Repo: repo.Name, Path: prefix + "/package/" + pid + "/" + rev.Revision + "/" + f.name,
					URL: s.http.resolve(base + "/files/" + escapePath(f.name)), Size: f.size, Created: when, Extra: map[string]string{},
				})
			}
		}
	}

	return out, nil
}

type conanListed struct {
	name string
	size int64
}

// conanFiles lists the files of a recipe or package revision, by name.
func (s *openartifactSource) conanFiles(ctx context.Context, revision string) ([]conanListed, error) {
	var resp struct {
		Files map[string]struct {
			Size int64 `json:"size"`
		} `json:"files"`
	}

	switch err := s.http.getJSON(ctx, s.http.resolve(revision+"/files"), &resp); {
	case errors.Is(err, errNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("files of %s: %w", redactURL(revision), err)
	}

	out := make([]conanListed, 0, len(resp.Files))
	for _, name := range slices.Sorted(maps.Keys(resp.Files)) {
		out = append(out, conanListed{name: name, size: resp.Files[name].Size})
	}

	return out, nil
}
