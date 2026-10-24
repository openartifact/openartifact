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
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const ciUploadUsage = "Usage: oa upload <repo> <file|glob>... [--target-path P] [--flat] [--property k=v]... " +
	"[--build-name N] [--build-number M] [--build-url U] [--vcs-revision SHA]"

// Property keys `oa upload` records for the build that produced an artifact;
// `oa bundle add --from-build` selects on the first two.
const (
	propBuildName   = "build.name"
	propBuildNumber = "build.number"
	propBuildURL    = "build.url"
	propVCSRevision = "vcs.revision"
)

var (
	errUploadFormat   = errors.New("format not supported by oa upload")
	errUploadNotFound = errors.New("no file matches")
	errUploadPath     = errors.New("invalid target path")
	errUploadNotFile  = errors.New("not a regular file")
	errUploadDigest   = errors.New("server stored different content than was sent")
	errUploadNoID     = errors.New("cannot determine the id of the uploaded artifact")
)

// ciUploadOptions is a parsed `oa upload` command line.
type ciUploadOptions struct {
	repo        string
	patterns    []string
	targetPath  string
	flat        bool
	properties  []string // key=value as given
	buildName   string
	buildNumber string
	buildURL    string
	vcsRevision string
}

// props returns the properties to set on every uploaded artifact: the build
// ones first, the custom ones after (and so winning on a repeated key).
func (o ciUploadOptions) props() map[string]string {
	p := map[string]string{}

	for k, v := range map[string]string{
		propBuildName: o.buildName, propBuildNumber: o.buildNumber, propBuildURL: o.buildURL, propVCSRevision: o.vcsRevision,
	} {
		if v != "" {
			p[k] = v
		}
	}

	for _, kv := range o.properties {
		k, v, _ := strings.Cut(kv, "=")
		p[k] = v
	}

	return p
}

// parseCIUploadArgs parses the command line of `oa upload`. Flags may appear
// before, between and after the positional arguments.
func parseCIUploadArgs(args []string, output io.Writer) (ciUploadOptions, error) {
	flags := flag.NewFlagSet("upload", flag.ContinueOnError)
	flags.SetOutput(output)

	var o ciUploadOptions

	flags.StringVar(&o.targetPath, "target-path", "", "Path in the repository; a trailing / makes it a directory (default: the file's path)")
	flags.BoolVar(&o.flat, "flat", false, "Drop the local directories: upload every file by its name")
	flags.Func("property", "Property k=v set on every uploaded artifact (repeatable)", func(s string) error {
		if k, _, ok := strings.Cut(s, "="); !ok || k == "" {
			return fmt.Errorf("%w: --property wants key=value, got %q", errCIUsage, s)
		}

		o.properties = append(o.properties, s)

		return nil
	})
	flags.StringVar(&o.buildName, "build-name", "", "Build name, recorded as property "+propBuildName)
	flags.StringVar(&o.buildNumber, "build-number", "", "Build number, recorded as property "+propBuildNumber)
	flags.StringVar(&o.buildURL, "build-url", "", "Build URL, recorded as property "+propBuildURL)
	flags.StringVar(&o.vcsRevision, "vcs-revision", "", "Source revision, recorded as property "+propVCSRevision)

	var positional []string

	for rest := args; ; {
		if err := flags.Parse(rest); err != nil {
			return ciUploadOptions{}, err
		}

		if flags.NArg() == 0 {
			break
		}

		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}

	if len(positional) < 2 {
		return ciUploadOptions{}, ciUsagef("%s", ciUploadUsage)
	}

	o.repo, o.patterns = positional[0], positional[1:]

	return o, nil
}

// ciUploadedFile is one artifact `oa upload` stored.
type ciUploadedFile struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
	ID     string `json:"id,omitempty"`
}

// ciUploadResult is the document of `oa upload --format json`. Error is only
// set when the upload stopped early; Uploaded then lists what was stored before.
type ciUploadResult struct {
	Uploaded []ciUploadedFile `json:"uploaded"`
	Error    string           `json:"error,omitempty"`
}

func handleCIUpload(args []string, client *Client, outFmt string) {
	o, err := parseCIUploadArgs(args, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}

	if err != nil {
		ciFail(err)
	}

	res, err := runCIUpload(context.Background(), newCIAPI(client), o)
	if err != nil {
		if outFmt == "json" {
			res.Error = err.Error()
			printJSON(res)
		}

		ciFail(err)
	}

	var text strings.Builder

	for _, u := range res.Uploaded {
		fmt.Fprintf(&text, "Uploaded %s/%s (%d bytes, sha256 %s)\n", u.Repo, u.Path, u.Size, u.SHA256)
	}

	ciPrint(outFmt, res, strings.TrimSuffix(text.String(), "\n"))
}

// uploadable reports whether `oa upload` can store into the format: the
// path-addressed ones and the two with an upload endpoint that reads package
// metadata.
func uploadable(f importFormat) bool {
	return pathAddressed(f) || f == formatDeb || f == formatRPM
}

// pathAddressed reports whether the format stores a file at the path it is
// uploaded to (the others derive the address from the package).
func pathAddressed(f importFormat) bool {
	return f == formatRaw || f == formatMaven || f == formatIvy || f == formatSBT
}

// runCIUpload uploads the files of o and records their properties. It stops at
// the first failure and returns what was stored up to then with the error.
func runCIUpload(ctx context.Context, api *ciAPI, o ciUploadOptions) (ciUploadResult, error) {
	res := ciUploadResult{Uploaded: []ciUploadedFile{}}

	repo, err := api.repo(ctx, o.repo)
	if err != nil {
		return res, err
	}

	f := importFormat(repo.Format)
	if !uploadable(f) {
		return res, fmt.Errorf("%w: repository %q is %s; use the native client (%s repositories take their packages from npm publish, docker push, twine and the like)",
			errUploadFormat, o.repo, repo.Format, repo.Format)
	}

	if !repo.hosted() {
		return res, fmt.Errorf("%w: repository %q is a %s repository, not a hosted one", errUploadFormat, o.repo, repo.RepoType)
	}

	if o.targetPath != "" && !pathAddressed(f) {
		return res, ciUsagef("--target-path does not apply to %s repositories: the server derives the address from the package", repo.Format)
	}

	files, err := expandUploadFiles(o.patterns)
	if err != nil {
		return res, err
	}

	paths, err := remotePaths(files, o, f)
	if err != nil {
		return res, err
	}

	props := o.props()

	for i, file := range files {
		u, err := uploadCIFile(ctx, api, repo, file, paths[i], props)
		if err != nil {
			return res, fmt.Errorf("%s: %w", file, err)
		}

		res.Uploaded = append(res.Uploaded, u)
	}

	return res, nil
}

// uploadCIFile stores one file, verifies the stored digest and sets props.
func uploadCIFile(ctx context.Context, api *ciAPI, repo ciRepo, file, remote string, props map[string]string) (ciUploadedFile, error) {
	f := importFormat(repo.Format)

	src, err := os.Open(file)
	if err != nil {
		return ciUploadedFile{}, err
	}

	defer func() { _ = src.Close() }()

	sum, size, err := hashFile(src)
	if err != nil {
		return ciUploadedFile{}, err
	}

	stored, err := api.t.upload(ctx, f, repo.Name, sourceAsset{Path: remote, Size: size, SHA256: sum}, src, uploadMeta{})
	if err != nil {
		return ciUploadedFile{}, err
	}

	if f == formatMaven && stored.SHA256 == "" {
		if stored.SHA256, err = api.t.serverSHA256(ctx, repo.Name, remote); err != nil {
			return ciUploadedFile{}, fmt.Errorf("verifying the stored file: %w", err)
		}
	}

	if stored.SHA256 != "" && stored.SHA256 != sum {
		return ciUploadedFile{}, fmt.Errorf("%w: sent sha256 %s, stored %s", errUploadDigest, sum, stored.SHA256)
	}

	if stored.ID == "" && len(props) > 0 {
		e, ok, ferr := api.t.findBySHA(ctx, repo.Name, f, sum)
		if ferr != nil {
			return ciUploadedFile{}, ferr
		}

		if ok {
			stored.ID = e.ID
		}
	}

	if len(props) > 0 {
		if stored.ID == "" {
			return ciUploadedFile{}, errUploadNoID
		}

		if err := api.t.setProperties(ctx, repo.Name, stored.ID, props); err != nil {
			return ciUploadedFile{}, fmt.Errorf("setting properties: %w", err)
		}
	}

	return ciUploadedFile{
		Repo: repo.Name, Path: remote, SHA256: sum, Size: size, ID: stored.ID,
		URL: uploadURL(api.t.c.baseURL, repo, remote, stored.ID),
	}, nil
}

// uploadURL is where the uploaded artifact can be fetched from.
func uploadURL(base string, repo ciRepo, remote, id string) string {
	if pathAddressed(importFormat(repo.Format)) {
		return base + "/repos/" + repo.Format + "/" + url.PathEscape(repo.Name) + "/" + escapePath(remote)
	}

	if id == "" {
		return ""
	}

	return base + "/api/v1/repositories/" + url.PathEscape(repo.Name) + "/packages/" + url.PathEscape(id) + "/download"
}

// remotePaths decides the repository path of every file. Deb and rpm
// repositories choose the address themselves: the path is the file name.
func remotePaths(files []string, o ciUploadOptions, f importFormat) ([]string, error) {
	out := make([]string, len(files))

	for i, file := range files {
		if !pathAddressed(f) {
			out[i] = filepath.Base(file)
			continue
		}

		p, err := remotePath(file, o, len(files) == 1)
		if err != nil {
			return nil, err
		}

		out[i] = p
	}

	return out, nil
}

// remotePath maps a local file to its repository path. Without --flat the
// file's own (relative) path is kept below the target path; --target-path with
// no trailing slash names the file itself when it is the only one.
func remotePath(file string, o ciUploadOptions, single bool) (string, error) {
	rel := filepath.ToSlash(filepath.Clean(file))
	if o.flat {
		rel = path.Base(rel)
	}

	if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%w: %s lies outside the working directory; give a relative path or use --flat", errUploadPath, file)
	}

	target := strings.TrimPrefix(filepath.ToSlash(o.targetPath), "/")

	var p string

	switch {
	case target == "":
		p = rel
	case strings.HasSuffix(target, "/"):
		p = target + rel
	case single:
		p = target
	default:
		p = target + "/" + rel
	}

	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: %q has an empty, . or .. segment", errUploadPath, p)
		}
	}

	return p, nil
}

// expandUploadFiles resolves the file and glob arguments to a sorted, de-duplicated
// list of regular files. A glob understands *, ?, [set] and ** (any number of
// directories); an argument without wildcards must name a file.
func expandUploadFiles(patterns []string) ([]string, error) {
	seen := map[string]bool{}

	var files []string

	add := func(p string) {
		if p = filepath.Clean(p); !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}

	for _, pat := range patterns {
		if !strings.ContainsAny(pat, "*?[") {
			info, err := os.Stat(pat)
			if err != nil {
				return nil, err
			}

			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("%w: %s (use a glob such as %s/** for a directory)", errUploadNotFile, pat, strings.TrimSuffix(pat, "/"))
			}

			add(pat)

			continue
		}

		matches, err := globFiles(pat)
		if err != nil {
			return nil, err
		}

		if len(matches) == 0 {
			return nil, fmt.Errorf("%w: %s", errUploadNotFound, pat)
		}

		for _, m := range matches {
			add(m)
		}
	}

	slices.Sort(files)

	return files, nil
}

// globFiles returns the regular files matching pattern, walking from the
// longest directory prefix that holds no wildcard.
func globFiles(pattern string) ([]string, error) {
	re, err := globRegexp(filepath.ToSlash(pattern))
	if err != nil {
		return nil, err
	}

	root := globRoot(filepath.ToSlash(pattern))

	var out []string

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		if slashed := filepath.ToSlash(p); re.MatchString(slashed) && d.Type().IsRegular() {
			out = append(out, p)
		}

		return nil
	})

	return out, err
}

// globRoot is the directory a glob starts at: its leading segments without wildcards.
func globRoot(pattern string) string {
	segs := strings.Split(pattern, "/")

	var keep []string

	for _, s := range segs[:len(segs)-1] {
		if strings.ContainsAny(s, "*?[") {
			break
		}

		keep = append(keep, s)
	}

	switch {
	case len(keep) == 0:
		return "."
	case len(keep) == 1 && keep[0] == "":
		return "/"
	}

	return strings.Join(keep, "/")
}

// globRegexp compiles a glob: * and ? stay within a path segment, ** crosses
// segments (a following / is optional so a/**/b matches a/b), [set] and [!set]
// are character classes.
func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder

	b.WriteString("^")

	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++

				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++

					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				return nil, ciUsagef("glob %q has an unclosed [", pattern)
			}

			class := pattern[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}

			b.WriteString("[" + class + "]")

			i += end + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}

	b.WriteString("$")

	return regexp.Compile(b.String())
}
