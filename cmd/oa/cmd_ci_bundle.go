// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
)

const (
	bundleUsage = "Usage: oa bundle <create|add|seal|show> <name> <version> [flags]\n" +
		"  oa bundle create <name> <version> [--description D] [--pipeline P]\n" +
		"  oa bundle add <name> <version> --repo R --path P... | --from-build NAME/NUMBER [--repo R]...\n" +
		"  oa bundle seal <name> <version> [--gpg-key K]\n" +
		"  oa bundle show <name> <version>"
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
	return "/api/v1/bundles/" + url.PathEscape(name) + "/" + url.PathEscape(version)
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

// ciFlags builds a flag set whose errors and usage go to stderr.
func ciFlags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(os.Stderr)

	return f
}

// parseInterspersed parses fs over args allowing flags between and after the
// positional arguments, and returns the positional ones.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string

	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}

		if fs.NArg() == 0 {
			return positional, nil
		}

		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
}

func handleBundle(args []string, client *Client, outFmt string) {
	if len(args) == 0 {
		ciFail(ciUsagef("%s", bundleUsage))
	}

	api := newCIAPI(client)
	ctx := context.Background()

	var (
		out bundleOut
		err error
	)

	switch args[0] {
	case "create":
		out, err = bundleCreateCmd(ctx, api, args[1:])
	case "add":
		out, err = bundleAddCmd(ctx, api, args[1:])
	case "seal":
		out, err = bundleSealCmd(ctx, api, args[1:])
	case "show":
		out, err = bundleShowCmd(ctx, api, args[1:])
	default:
		err = ciUsagef("unknown bundle subcommand %q\n%s", args[0], bundleUsage)
	}

	if errors.Is(err, flag.ErrHelp) {
		return
	}

	if err != nil {
		ciFail(err)
	}

	ciPrint(outFmt, out, out.text())
}

func bundleCreateCmd(ctx context.Context, api *ciAPI, args []string) (bundleOut, error) {
	fs := ciFlags("bundle create")
	description := fs.String("description", "", "Bundle description")
	pipeline := fs.String("pipeline", "", "Promotion pipeline, by name (needed to promote the bundle later)")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return bundleOut{}, err
	}

	if len(pos) != 2 {
		return bundleOut{}, ciUsagef("Usage: oa bundle create <name> <version> [--description D] [--pipeline P]")
	}

	body := map[string]any{"name": pos[0], "version": pos[1], "description": *description}
	if *pipeline != "" {
		body["pipeline"] = *pipeline
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

func parseBundleAddArgs(args []string) (bundleAddOptions, error) {
	fs := ciFlags("bundle add")

	var o bundleAddOptions

	fs.Func("repo", "Repository of the artifacts (repeatable with --from-build to limit the search)", func(s string) error {
		o.repos = append(o.repos, s)
		return nil
	})
	fs.Func("path", "Artifact path in --repo (repeatable)", func(s string) error {
		o.paths = append(o.paths, s)
		return nil
	})
	fs.StringVar(&o.fromBuild, "from-build", "", "Add every artifact recorded for build NAME/NUMBER (properties "+propBuildName+" and "+propBuildNumber+")")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return o, err
	}

	if len(pos) != 2 {
		return o, ciUsagef("Usage: oa bundle add <name> <version> --repo R --path P... | --from-build NAME/NUMBER [--repo R]...")
	}

	o.name, o.version = pos[0], pos[1]

	switch {
	case o.fromBuild != "" && len(o.paths) > 0:
		return o, ciUsagef("--from-build and --path are exclusive")
	case o.fromBuild != "":
		if i := strings.LastIndexByte(o.fromBuild, '/'); i <= 0 || i == len(o.fromBuild)-1 {
			return o, ciUsagef("--from-build wants NAME/NUMBER, got %q", o.fromBuild)
		}
	case len(o.paths) == 0:
		return o, ciUsagef("give --path (with --repo) or --from-build")
	case len(o.repos) != 1:
		return o, ciUsagef("--path needs exactly one --repo")
	}

	return o, nil
}

// bundleSource is an artifact to add to a bundle: a row of a repository's listing.
type bundleSource struct {
	repo ciRepo
	pkg  ciPackage
	path string
}

func bundleAddCmd(ctx context.Context, api *ciAPI, args []string) (bundleOut, error) {
	o, err := parseBundleAddArgs(args)
	if err != nil {
		return bundleOut{}, err
	}

	var sources []bundleSource

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

		listed := c.filename
		if f == formatIvy {
			listed = ivyListedFilename(c.filename)
		}

		return c.version, func(row ciPackage) bool {
			return row.Filename == listed && row.Version == c.version && row.Name == c.group+":"+c.module
		}, nil
	case formatDeb, formatRPM:
		return "", func(c ciPackage) bool { return c.Filename == path.Base(p) }, nil
	}

	return "", nil, fmt.Errorf("%w: --path works for raw, maven, ivy, sbt, deb and rpm repositories, not %s (use --from-build)", errBundlePath, f)
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
		return nil, fmt.Errorf("%w: no artifact carries %s=%s and %s=%s", errBundleNoArtifacts, propBuildName, name, propBuildNumber, number)
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

func bundleSealCmd(ctx context.Context, api *ciAPI, args []string) (bundleOut, error) {
	fs := ciFlags("bundle seal")
	gpgKey := fs.String("gpg-key", "", "Sign the manifest with this GPG key (id as the server lists it)")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return bundleOut{}, err
	}

	if len(pos) != 2 {
		return bundleOut{}, ciUsagef("Usage: oa bundle seal <name> <version> [--gpg-key K]")
	}

	var body any
	if *gpgKey != "" {
		body = map[string]string{"gpg_key_id": *gpgKey}
	}

	var doc bundleDoc
	if err := api.call(ctx, http.MethodPost, bundleAPIPath(pos[0], pos[1])+"/seal", body, &doc); err != nil {
		return bundleOut{}, err
	}

	return api.bundleOut(doc), nil
}

func bundleShowCmd(ctx context.Context, api *ciAPI, args []string) (bundleOut, error) {
	pos, err := parseInterspersed(ciFlags("bundle show"), args)
	if err != nil {
		return bundleOut{}, err
	}

	if len(pos) != 2 {
		return bundleOut{}, ciUsagef("Usage: oa bundle show <name> <version>")
	}

	base := bundleAPIPath(pos[0], pos[1])

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

		out.Items = append(out.Items, bundleItemOut{Repo: repo, Path: it.ArtifactPath, SHA256: it.SHA256, Size: it.SizeBytes, Format: it.Format})
	}

	return out, nil
}

func handlePromote(args []string, client *Client, outFmt string) {
	out, err := promoteCmd(context.Background(), newCIAPI(client), args, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}

	if err != nil {
		ciFail(err)
	}

	ciPrint(outFmt, out, out.text())
}

func promoteCmd(ctx context.Context, api *ciAPI, args []string, stderr io.Writer) (bundleOut, error) {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	fs.SetOutput(stderr)

	stage := fs.String("stage", "", "Pipeline stage to promote to (required)")
	target := fs.String("target-repo", "", "Repository to promote into (default: the one the stage fixes)")
	notes := fs.String("notes", "", "Promotion notes")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return bundleOut{}, err
	}

	if len(pos) != 2 || *stage == "" {
		return bundleOut{}, ciUsagef("%s", promoteUsage)
	}

	base := bundleAPIPath(pos[0], pos[1])

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

	body := map[string]string{"to_stage": *stage, "target_repo": *target, "notes": *notes}
	if err := api.call(ctx, http.MethodPost, base+"/promote", body, &report); err != nil {
		return bundleOut{}, err
	}

	var doc bundleDoc
	if err := api.call(ctx, http.MethodGet, base, nil, &doc); err != nil {
		return bundleOut{}, err
	}

	targetName := *target
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
