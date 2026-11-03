// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	promoteUsage = "Usage: oa promote <name> <version> --stage S [--target-repo R] [--notes T]"

	// maxItemsPerRequest is the most items the server takes in one add request.
	maxItemsPerRequest = 500
)

var (
	errBundleNoArtifacts = errors.New("no artifacts to add")
	errBundleAmbiguous   = errors.New("more than one artifact matches")
	errBundlePath        = errors.New("cannot add by path")
	errBundleNotFound    = errors.New("no such artifact")
	errBundleUnlisted    = errors.New("artifacts missing from the package listing")
)

// bundleItemOut is an item of a bundle as the CI commands print it.
type bundleItemOut struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Format string `json:"format,omitempty"`
}

// promotedOut is what a promotion did.
type promotedOut struct {
	Stage      string `json:"stage"`
	TargetRepo string `json:"target_repo"`
	Items      int    `json:"items"`
	Copied     int    `json:"copied"`
	Existing   int    `json:"existing"`
	Replaced   int    `json:"replaced"`
}

// bundleOut is the document every bundle command and promote print under
// --format json; the optional parts belong to one command each.
type bundleOut struct {
	Bundle       string          `json:"bundle"`
	Version      string          `json:"version"`
	State        string          `json:"state"`
	URL          string          `json:"url"`
	Description  string          `json:"description,omitempty"`
	PipelineID   string          `json:"pipeline_id,omitempty"`
	CurrentStage string          `json:"current_stage,omitempty"`
	CreatedBy    string          `json:"created_by,omitempty"`
	SealedAt     string          `json:"sealed_at,omitempty"`
	GPGKeyID     string          `json:"gpg_key_id,omitempty"`
	Added        *int            `json:"added,omitempty"`
	Items        []bundleItemOut `json:"items,omitempty"`
	Promoted     *promotedOut    `json:"promoted,omitempty"`
}

// bundleDoc is the server's representation of a bundle.
type bundleDoc struct {
	Name         string  `json:"name"`
	Version      string  `json:"version"`
	Description  string  `json:"description"`
	State        string  `json:"state"`
	PipelineID   *string `json:"pipeline_id"`
	CurrentStage *string `json:"current_stage"`
	CreatedBy    string  `json:"created_by"`
	SealedAt     *string `json:"sealed_at"`
	GPGKeyID     *string `json:"gpg_key_id"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

func (a *ciAPI) bundleOut(d bundleDoc) bundleOut {
	return bundleOut{
		Bundle: d.Name, Version: d.Version, State: d.State,
		URL:         a.t.c.baseURL + "/bundles/" + url.PathEscape(d.Name) + "/" + url.PathEscape(d.Version),
		Description: d.Description, PipelineID: deref(d.PipelineID), CurrentStage: deref(d.CurrentStage),
		CreatedBy: d.CreatedBy, SealedAt: deref(d.SealedAt), GPGKeyID: deref(d.GPGKeyID),
	}
}

func bundleAPIPath(name, version string) string {
	return "/api/v1/bundles/" + escape(name) + "/" + escape(version)
}

func (o bundleOut) text() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Bundle %s %s: %s", o.Bundle, o.Version, o.State)

	if o.CurrentStage != "" {
		fmt.Fprintf(&b, ", stage %s", o.CurrentStage)
	}

	if o.Added != nil {
		fmt.Fprintf(&b, "\nAdded %d item(s)", *o.Added)
	}

	for _, it := range o.Items {
		fmt.Fprintf(&b, "\n  %s/%s  %s  %d bytes", it.Repo, it.Path, it.SHA256, it.Size)
	}

	if p := o.Promoted; p != nil {
		fmt.Fprintf(&b, "\nPromoted to %s (%s): %d item(s), %d copied, %d already present, %d replaced",
			p.Stage, p.TargetRepo, p.Items, p.Copied, p.Existing, p.Replaced)
	}

	return b.String()
}

func newBundleCmd(a *app) *cobra.Command {
	return parentCmd("bundle", "Manage release bundles",
		bundleCreateCmd(a),
		bundleAddCmd(a),
		bundleSealCmd(a),
		bundleShowCmd(a),
	)
}

// bundleNameVersion completes `<name> <version>`: the names and versions are
// the user's to choose, nothing to suggest.
func bundleNameVersion() cobra.CompletionFunc {
	return positional(nothing, nothing)
}

// runBundle runs one bundle command body and prints its result like every CI command.
func runBundle(a *app, fn func(ctx context.Context, api *ciAPI) (bundleOut, error)) error {
	out, err := fn(context.Background(), newCIAPI(a.client))
	if err != nil {
		return err
	}

	ciPrint(a.outFmt, out, out.text())

	return nil
}

func bundleCreateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "create <name> <version>",
		Short:             "Create a draft release bundle",
		Args:              usageArgs(2, 2),
		ValidArgsFunction: bundleNameVersion(),
	}

	description := cmd.Flags().String("description", "", "Bundle description")
	pipeline := cmd.Flags().String("pipeline", "", "Promotion pipeline, by name (needed to promote the bundle later)")

	cmd.RunE = func(_ *cobra.Command, args []string) error {
		return runBundle(a, func(ctx context.Context, api *ciAPI) (bundleOut, error) {
			return bundleCreate(ctx, api, args[0], args[1], *description, *pipeline)
		})
	}

	return cmd
}

func bundleCreate(ctx context.Context, api *ciAPI, name, version, description, pipeline string) (bundleOut, error) {
	body := map[string]any{"name": name, "version": version, "description": description}
	if pipeline != "" {
		body["pipeline"] = pipeline
	}

	var doc bundleDoc
	if err := api.call(ctx, http.MethodPost, "/api/v1/bundles", body, &doc); err != nil {
		return bundleOut{}, err
	}

	return api.bundleOut(doc), nil
}

// bundleAddOptions is a parsed `oa bundle add` command line.
type bundleAddOptions struct {
	name, version string
	repos         []string
	paths         []string
	fromBuild     string
}

// bindBundleAddFlags declares the flags of `oa bundle add` on fs, storing into o.
func bindBundleAddFlags(fs *pflag.FlagSet, o *bundleAddOptions) {
	fs.StringArrayVarP(&o.repos, "repo", "r", nil, "Repository of the artifacts (repeatable with --from-build to limit the search)")
	fs.StringArrayVar(&o.paths, "path", nil, "Artifact path in --repo (repeatable)")
	fs.StringVar(
		&o.fromBuild,
		"from-build",
		"",
		"Add every artifact recorded for build NAME/NUMBER (properties "+propBuildName+" and "+propBuildNumber+")",
	)
}

// withArgs sets name and version from the positional arguments and checks that
// the flags describe one of the two ways to add artifacts.
func (o *bundleAddOptions) withArgs(args []string) error {
	o.name, o.version = args[0], args[1]

	switch {
	case o.fromBuild != "" && len(o.paths) > 0:
		return cliError("--from-build and --path are exclusive")
	case o.fromBuild != "":
		if i := strings.LastIndexByte(o.fromBuild, '/'); i <= 0 || i == len(o.fromBuild)-1 {
			return cliErrorf("--from-build wants NAME/NUMBER, got %q", o.fromBuild)
		}
	case len(o.paths) == 0:
		return cliError("give --path (with --repo) or --from-build")
	case len(o.repos) != 1:
		return cliError("--path needs exactly one --repo")
	}

	return nil
}

// bundleSource is an artifact to add to a bundle: a row of a repository's listing.
type bundleSource struct {
	repo ciRepo
	pkg  ciPackage
	path string
}

func bundleAddCmd(a *app) *cobra.Command {
	var o bundleAddOptions

	cmd := &cobra.Command{
		Use:   "add <name> <version>",
		Short: "Add artifacts to a draft bundle, by path or by the build that produced them",
		Long: "Add artifacts to a draft bundle: either --repo R with one or more --path P, or --from-build NAME/NUMBER " +
			"(optionally limited to the repositories named by --repo).",
		Args:              usageArgs(2, 2),
		ValidArgsFunction: bundleNameVersion(),
	}

	bindBundleAddFlags(cmd.Flags(), &o)
	flagValues(cmd, "repo", a.repoNames())

	cmd.RunE = func(_ *cobra.Command, args []string) error {
		if err := o.withArgs(args); err != nil {
			return err
		}

		return runBundle(a, func(ctx context.Context, api *ciAPI) (bundleOut, error) { return bundleAdd(ctx, api, o) })
	}

	return cmd
}

func bundleAdd(ctx context.Context, api *ciAPI, o bundleAddOptions) (bundleOut, error) {
	var (
		sources []bundleSource
		err     error
	)

	if o.fromBuild != "" {
		i := strings.LastIndexByte(o.fromBuild, '/')
		sources, err = sourcesFromBuild(ctx, api, o.repos, o.fromBuild[:i], o.fromBuild[i+1:])
	} else {
		sources, err = sourcesFromPaths(ctx, api, o.repos[0], o.paths)
	}

	if err != nil {
		return bundleOut{}, err
	}

	items := dedupeSources(sources)
	added := make([]bundleItemOut, 0, len(items))
	req := make([]map[string]any, 0, len(items))

	for _, s := range items {
		added = append(added, bundleItemOut{Repo: s.repo.Name, Path: s.path, SHA256: s.pkg.SHA256, Size: s.pkg.Size, Format: s.repo.Format})
		req = append(req, map[string]any{
			"source_repo_id": s.repo.ID, "sha256": s.pkg.SHA256, "artifact_path": s.path, "format": s.repo.Format, "size_bytes": s.pkg.Size,
		})
	}

	base := bundleAPIPath(o.name, o.version)

	for chunk := range slices.Chunk(req, maxItemsPerRequest) {
		if err := api.call(ctx, http.MethodPost, base+"/items", map[string]any{"items": chunk}, nil); err != nil {
			return bundleOut{}, err
		}
	}

	var doc bundleDoc
	if err := api.call(ctx, http.MethodGet, base, nil, &doc); err != nil {
		return bundleOut{}, err
	}

	out := api.bundleOut(doc)
	out.Added = new(len(added))
	out.Items = added

	return out, nil
}

// dedupeSources drops repeated (repository, digest) pairs: the server rejects an
// item twice in one request, and one copy of the content is what gets promoted.
func dedupeSources(in []bundleSource) []bundleSource {
	seen := map[string]bool{}
	out := make([]bundleSource, 0, len(in))

	for _, s := range in {
		key := s.repo.ID + "/" + s.pkg.SHA256
		if !seen[key] {
			seen[key] = true

			out = append(out, s)
		}
	}

	return out
}

// listedMatcher tells how to find the listing row of a path in a repository of
// format f: the text to narrow the listing by and the test for the row.
func listedMatcher(f importFormat, p string) (string, func(ciPackage) bool, error) {
	parts := strings.Split(p, "/")

	switch f {
	case formatRaw:
		return p, func(c ciPackage) bool { return c.Filename == p }, nil
	case formatMaven:
		if len(parts) < 2 {
			return "", nil, fmt.Errorf("%w: %q is not a Maven repository path", errBundlePath, p)
		}

		return parts[len(parts)-2], func(c ciPackage) bool { return c.Filename == p }, nil
	case formatIvy, formatSBT:
		c, ok := splitIvySBTPath(p)
		if !ok {
			return "", nil, fmt.Errorf("%w: %q is not a %s repository path", errBundlePath, p, f)
		}

		// The listing names the stored file name. An ivy server before the generic
		// package tables listed it with its extension appended again.
		return c.version, func(row ciPackage) bool {
			named := row.Filename == c.filename || f == formatIvy && slices.Contains(ivyListedFilenames(c.filename), row.Filename)

			return named && row.Version == c.version && row.Name == c.group+":"+c.module
		}, nil
	case formatDeb, formatRPM:
		return "", func(c ciPackage) bool { return c.Filename == path.Base(p) }, nil
	}

	return "", nil, fmt.Errorf(
		"%w: --path works for deb, ivy, maven, raw, rpm and sbt repositories, not %s (use --from-build)",
		errBundlePath,
		f,
	)
}

func sourcesFromPaths(ctx context.Context, api *ciAPI, repoName string, paths []string) ([]bundleSource, error) {
	repo, err := api.repo(ctx, repoName)
	if err != nil {
		return nil, err
	}

	var out []bundleSource

	for _, p := range paths {
		p = strings.TrimPrefix(p, "/")

		q, match, err := listedMatcher(importFormat(repo.Format), p)
		if err != nil {
			return nil, err
		}

		var found []ciPackage

		if err := api.packages(ctx, repo.Name, q, func(c ciPackage) bool {
			if match(c) {
				found = append(found, c)
			}

			return true
		}); err != nil {
			return nil, err
		}

		switch len(found) {
		case 0:
			return nil, fmt.Errorf("%w: %s/%s", errBundleNotFound, repo.Name, p)
		case 1:
			out = append(out, bundleSource{repo: repo, pkg: found[0], path: p})
		default:
			return nil, fmt.Errorf("%w: %s/%s", errBundleAmbiguous, repo.Name, p)
		}
	}

	return out, nil
}

// sourcesFromBuild finds the artifacts carrying build.name=name and
// build.number=number in the given repositories (default: every hosted
// repository the caller can read).
func sourcesFromBuild(ctx context.Context, api *ciAPI, names []string, name, number string) ([]bundleSource, error) {
	repos, err := searchableRepos(ctx, api, names)
	if err != nil {
		return nil, err
	}

	var out []bundleSource

	for _, repo := range repos {
		ids, err := buildArtifactIDs(ctx, api, repo.Name, name, number)
		if err != nil {
			return nil, err
		}

		if len(ids) == 0 {
			continue
		}

		err = api.packages(ctx, repo.Name, "", func(c ciPackage) bool {
			if ids[c.ID] {
				out = append(out, bundleSource{repo: repo, pkg: c, path: c.Filename})
				delete(ids, c.ID)
			}

			return len(ids) > 0
		})
		if err != nil {
			return nil, err
		}

		if len(ids) > 0 {
			return nil, fmt.Errorf("%w: %s: %d of build %s/%s", errBundleUnlisted, repo.Name, len(ids), name, number)
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf(
			"%w: no artifact carries %s=%s and %s=%s",
			errBundleNoArtifacts,
			propBuildName,
			name,
			propBuildNumber,
			number,
		)
	}

	return out, nil
}

// searchableRepos resolves the repositories to search: the named ones, or every
// hosted repository the caller can read.
func searchableRepos(ctx context.Context, api *ciAPI, names []string) ([]ciRepo, error) {
	if len(names) > 0 {
		out := make([]ciRepo, 0, len(names))

		for _, n := range names {
			r, err := api.repo(ctx, n)
			if err != nil {
				return nil, err
			}

			out = append(out, r)
		}

		return out, nil
	}

	all, err := api.repos(ctx)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(all, func(r ciRepo) bool { return !r.hosted() }), nil
}

// buildArtifactIDs returns the ids of the artifacts of repo recorded for the build.
func buildArtifactIDs(ctx context.Context, api *ciAPI, repo, name, number string) (map[string]bool, error) {
	byName, err := api.searchProperty(ctx, repo, propBuildName, name)
	if err != nil || len(byName) == 0 {
		return nil, err
	}

	byNumber, err := api.searchProperty(ctx, repo, propBuildNumber, number)
	if err != nil {
		return nil, err
	}

	ids := map[string]bool{}

	for _, id := range byName {
		if slices.Contains(byNumber, id) {
			ids[id] = true
		}
	}

	return ids, nil
}

func bundleSealCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "seal <name> <version>",
		Short:             "Seal a bundle (optionally sign its manifest)",
		Args:              usageArgs(2, 2),
		ValidArgsFunction: bundleNameVersion(),
	}

	gpgKey := cmd.Flags().String("gpg-key", "", "Sign the manifest with this GPG key (id as the server lists it)")

	cmd.RunE = func(_ *cobra.Command, args []string) error {
		return runBundle(a, func(ctx context.Context, api *ciAPI) (bundleOut, error) {
			return bundleSeal(ctx, api, args[0], args[1], *gpgKey)
		})
	}

	return cmd
}

func bundleSeal(ctx context.Context, api *ciAPI, name, version, gpgKey string) (bundleOut, error) {
	var body any
	if gpgKey != "" {
		body = map[string]string{"gpg_key_id": gpgKey}
	}

	var doc bundleDoc
	if err := api.call(ctx, http.MethodPost, bundleAPIPath(name, version)+"/seal", body, &doc); err != nil {
		return bundleOut{}, err
	}

	return api.bundleOut(doc), nil
}

func bundleShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:               "show <name> <version>",
		Short:             "Show a bundle and its items",
		Args:              usageArgs(2, 2),
		ValidArgsFunction: bundleNameVersion(),
		RunE: func(_ *cobra.Command, args []string) error {
			return runBundle(a, func(ctx context.Context, api *ciAPI) (bundleOut, error) {
				return bundleShow(ctx, api, args[0], args[1])
			})
		},
	}
}

func bundleShow(ctx context.Context, api *ciAPI, name, version string) (bundleOut, error) {
	base := bundleAPIPath(name, version)

	var doc bundleDoc
	if err := api.call(ctx, http.MethodGet, base, nil, &doc); err != nil {
		return bundleOut{}, err
	}

	var list struct {
		Items []struct {
			SourceRepoID string `json:"source_repo_id"`
			SHA256       string `json:"sha256"`
			ArtifactPath string `json:"artifact_path"`
			Format       string `json:"format"`
			SizeBytes    int64  `json:"size_bytes"`
		} `json:"items"`
	}

	if err := api.call(ctx, http.MethodGet, base+"/items", nil, &list); err != nil {
		return bundleOut{}, err
	}

	names := map[string]string{}

	if repos, err := api.repos(ctx); err == nil {
		for _, r := range repos {
			names[r.ID] = r.Name
		}
	}

	out := api.bundleOut(doc)

	for _, it := range list.Items {
		repo := names[it.SourceRepoID]
		if repo == "" {
			repo = it.SourceRepoID
		}

		out.Items = append(
			out.Items,
			bundleItemOut{Repo: repo, Path: it.ArtifactPath, SHA256: it.SHA256, Size: it.SizeBytes, Format: it.Format},
		)
	}

	return out, nil
}

func newPromoteCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "promote <name> <version> --stage S",
		Short:             "Promote a sealed bundle to a pipeline stage",
		Args:              usageArgs(2, 2),
		ValidArgsFunction: bundleNameVersion(),
	}

	stage := cmd.Flags().String("stage", "", "Pipeline stage to promote to (required)")
	target := cmd.Flags().String("target-repo", "", "Repository to promote into (default: the one the stage fixes)")
	notes := cmd.Flags().String("notes", "", "Promotion notes")

	flagValues(cmd, "target-repo", a.repoNames())

	cmd.RunE = func(_ *cobra.Command, args []string) error {
		if *stage == "" {
			return cliError(promoteUsage)
		}

		return runBundle(a, func(ctx context.Context, api *ciAPI) (bundleOut, error) {
			return promote(ctx, api, args[0], args[1], *stage, *target, *notes)
		})
	}

	return cmd
}

func promote(ctx context.Context, api *ciAPI, name, version, stage, target, notes string) (bundleOut, error) {
	base := bundleAPIPath(name, version)

	var report struct {
		Promotion struct {
			ToStage      string `json:"to_stage"`
			TargetRepoID string `json:"target_repo_id"`
			ItemCount    int    `json:"item_count"`
		} `json:"promotion"`
		Copied   int `json:"copied"`
		Existing int `json:"existing"`
		Replaced int `json:"replaced"`
	}

	body := map[string]string{"to_stage": stage, "target_repo": target, "notes": notes}
	if err := api.call(ctx, http.MethodPost, base+"/promote", body, &report); err != nil {
		return bundleOut{}, err
	}

	var doc bundleDoc
	if err := api.call(ctx, http.MethodGet, base, nil, &doc); err != nil {
		return bundleOut{}, err
	}

	targetName := target
	if targetName == "" {
		targetName = api.repoName(ctx, report.Promotion.TargetRepoID)
	}

	out := api.bundleOut(doc)
	out.Promoted = &promotedOut{
		Stage: report.Promotion.ToStage, TargetRepo: targetName, Items: report.Promotion.ItemCount,
		Copied: report.Copied, Existing: report.Existing, Replaced: report.Replaced,
	}

	return out, nil
}

// repoName resolves a repository id to its name through the readable
// repositories; an id nobody can read is returned as is.
func (a *ciAPI) repoName(ctx context.Context, id string) string {
	if repos, err := a.repos(ctx); err == nil {
		for _, r := range repos {
			if r.ID == id {
				return r.Name
			}
		}
	}

	return id
}
