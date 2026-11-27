// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

const (
	cpUsage = "Usage: oa cp <source> <target> <package>@<version>... [--dry-run] [--force]  |  oa cp <source> <target> --sha256 HEX [--path P]"
	mvUsage = "Usage: oa mv <source> <target> <package>@<version>... [--dry-run] [--force]  |  oa mv <source> <target> --sha256 HEX [--path P]"
)

// transferItem is an item of the copy and move requests.
type transferItem struct {
	Package string `json:"package,omitempty"`
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Path    string `json:"path,omitempty"`
}

// transferArtifact is an artifact of the report.
type transferArtifact struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// transferOut is the document `oa cp` and `oa mv` print under --format json.
type transferOut struct {
	Operation string `json:"operation"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	DryRun    bool   `json:"dry_run"`
	Copied    int    `json:"copied"`
	Existing  int    `json:"existing"`
	Replaced  int    `json:"replaced"`
	Moved     int    `json:"moved,omitempty"`
	Items     []struct {
		Package      string             `json:"package,omitempty"`
		Version      string             `json:"version,omitempty"`
		SHA256       string             `json:"sha256,omitempty"`
		ArtifactPath string             `json:"artifact_path,omitempty"`
		Copied       []transferArtifact `json:"copied"`
		Existing     []transferArtifact `json:"existing"`
		Replaced     []transferArtifact `json:"replaced"`
	} `json:"items"`
}

func (o transferOut) text() string {
	var b strings.Builder

	verb := map[string]string{"copy": "copied", "move": "moved"}[o.Operation]
	if verb == "" {
		verb = o.Operation
	}

	if o.DryRun {
		fmt.Fprintf(&b, "Dry run, nothing written. Would have %s %s to %s:\n", verb, o.Source, o.Target)
	} else {
		fmt.Fprintf(&b, "%s %s to %s:\n", strings.ToUpper(verb[:1])+verb[1:], o.Source, o.Target)
	}

	for _, it := range o.Items {
		for _, group := range []struct {
			label string
			list  []transferArtifact
		}{{"copy", it.Copied}, {"exists", it.Existing}, {"replace", it.Replaced}} {
			for _, a := range group.list {
				name := a.Name
				if a.Version != "" {
					name += "@" + a.Version
				}

				fmt.Fprintf(&b, "  %-8s %s  %s\n", group.label, name, a.Path)
			}
		}
	}

	fmt.Fprintf(&b, "%d copied, %d already in the target, %d replaced", o.Copied, o.Existing, o.Replaced)

	if o.Operation == "move" && !o.DryRun {
		fmt.Fprintf(&b, ", %d moved to the source's trash", o.Moved)
	}

	return b.String()
}

// parsePackageVersion splits <package>@<version> at the last "@" (npm scopes
// start with one).
func parsePackageVersion(s string) (transferItem, error) {
	i := strings.LastIndex(s, "@")
	if i <= 0 || i == len(s)-1 {
		return transferItem{}, cliErrorf("%q is not <package>@<version>", s)
	}

	return transferItem{Package: s[:i], Version: s[i+1:]}, nil
}

func newCpCmd(a *app) *cobra.Command {
	return newTransferCmd(a, false)
}

func newMvCmd(a *app) *cobra.Command {
	return newTransferCmd(a, true)
}

func newTransferCmd(a *app, move bool) *cobra.Command {
	use, short, long, usage := "cp <source> <target> [package@version...]", "Copy artifacts to another hosted repository",
		"Copy the live files of package versions (or one artifact by --sha256) from a hosted repository into another "+
			"hosted repository of the same format, in one transaction and without moving any bytes. Properties are "+
			"copied; the target scans the copies as uploads. Needs read on the source and write on the target.", cpUsage

	if move {
		use, short, usage = "mv <source> <target> [package@version...]", "Move artifacts to another hosted repository", mvUsage
		long = "Copy, then take the source files to the source repository's trash (restorable with `oa trash restore`). " +
			"Needs read and delete on the source and write on the target. A source artifact held by a sealed " +
			"release bundle, or a release of a repository with immutable releases, is refused."
	}

	cmd := &cobra.Command{
		Use:               use,
		Short:             short,
		Long:              long,
		Args:              usageArgs(2, -1),
		ValidArgsFunction: positional(a.repoNames(), a.repoNames()),
	}

	sha := cmd.Flags().
		String("sha256", "", "Select an artifact by the sha256 of its stored file (docker: of the manifest) instead of package@version")
	path := cmd.Flags().String("path", "", "With --sha256: the artifact path (docker: the tag) to pick among artifacts sharing the digest")
	dryRun := cmd.Flags().Bool("dry-run", false, "Show what would happen; write nothing")
	force := cmd.Flags().Bool("force", false, "Administrators: replace conflicting content in the target (it goes to the target's trash)")

	cmd.RunE = func(_ *cobra.Command, args []string) error {
		items, err := transferItems(args[2:], *sha, *path)
		if err != nil {
			return cliError(usage + "\n" + err.Error())
		}

		out, err := transfer(a.client.context(), newCIAPI(a.client), move, args[0], args[1], items, *dryRun, *force)
		if err != nil {
			return err
		}

		ciPrint(a.outFmt, out, out.text())

		return nil
	}

	return cmd
}

// transferItems builds the items of the request from the arguments and flags.
func transferItems(args []string, sha, path string) ([]transferItem, error) {
	var items []transferItem

	for _, arg := range args {
		it, err := parsePackageVersion(arg)
		if err != nil {
			return nil, err
		}

		items = append(items, it)
	}

	switch {
	case sha != "":
		items = append(items, transferItem{SHA256: sha, Path: path})
	case path != "":
		return nil, cliError("--path needs --sha256")
	}

	if len(items) == 0 {
		return nil, cliError("name at least one <package>@<version> or --sha256")
	}

	return items, nil
}

func transfer(
	ctx context.Context,
	api *ciAPI,
	move bool,
	source, target string,
	items []transferItem,
	dryRun, force bool,
) (transferOut, error) {
	op := "copy"
	if move {
		op = "move"
	}

	body := map[string]any{"target": target, "items": items, "dry_run": dryRun, "force": force}

	var out transferOut
	if err := api.call(ctx, http.MethodPost, "/api/v1/repositories/"+escape(source)+"/"+op, body, &out); err != nil {
		return transferOut{}, err
	}

	return out, nil
}
