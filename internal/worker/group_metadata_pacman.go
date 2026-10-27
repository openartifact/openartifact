// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/group"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/pkg/pacman"
)

// maxUpstreamPacmanDB caps the compressed upstream database (or files
// database) read for a merge. The decompressed size is bounded by pacman.ReadDB.
const maxUpstreamPacmanDB = 1 << 30

// errPacmanMembersUnreadable is returned for a requested architecture that no
// readable member has while some proxy member could not be read: the request is
// to be retried, not answered 404.
var errPacmanMembersUnreadable = errors.New("group worker: a proxy member could not be read")

// pacmanHosted is the data a hosted pacman member contributes: its packages and
// their file lists, rendered into entries per architecture.
type pacmanHosted struct {
	pkgs  []*models.PacmanPackage
	lists map[string]string
}

// pacmanContribution is the database of one member for one architecture.
type pacmanContribution struct {
	member string
	// proxy is the member when it is a proxy, nil for a hosted one.
	proxy   *models.Repository
	entries []pacman.DBEntry
}

// pacmanPick is the entry the merged database lists for a package name and the
// member that serves the package file.
type pacmanPick struct {
	member string
	entry  pacman.DBEntry
}

// mergePacman picks, from the members' databases of one architecture (in
// member order), the entry of every package name: a pacman database lists one
// version of a name.
//
// Hosted wins, as in deb and rpm groups: a name that any hosted member holds is
// hidden from the proxy members, whatever their order. Among the rest the first
// member in order that has the name wins. With mergeVersions (the group's
// merge_proxy_versions) hosted members no longer hide the proxies and the
// newest version of a name wins instead, the earlier member on a tie.
func mergePacman(contribs []pacmanContribution, mergeVersions bool) []pacmanPick {
	var hostedNames map[string]struct{}

	if !mergeVersions {
		hostedNames = make(map[string]struct{})

		for _, c := range contribs {
			if c.proxy != nil {
				continue
			}

			for i := range c.entries {
				hostedNames[c.entries[i].Name] = struct{}{}
			}
		}
	}

	picked := make(map[string]*pacmanPick)

	for _, c := range contribs {
		for i := range c.entries {
			e := &c.entries[i]

			if _, hidden := hostedNames[e.Name]; hidden && c.proxy != nil {
				continue
			}

			cur, ok := picked[e.Name]

			switch {
			case !ok:
				picked[e.Name] = &pacmanPick{member: c.member, entry: *e}
			case mergeVersions && pacman.Vercmp(e.Version, cur.entry.Version) > 0:
				cur.member, cur.entry = c.member, *e
			}
		}
	}

	out := make([]pacmanPick, 0, len(picked))
	for _, p := range picked {
		out = append(out, *p)
	}

	sort.Slice(out, func(a, b int) bool { return out[a].entry.Name < out[b].entry.Name })

	return out
}

// proxySection returns the repository ("section") name of a proxy member: the
// last path segment of its upstream URL, from which the member serves
// os/ARCH/<section>.db. ok is false when the upstream has no path.
func proxySection(m *models.Repository) (string, bool) {
	u, err := url.Parse(m.UpstreamURL)
	if err != nil {
		return "", false
	}

	seg := path.Base(strings.TrimRight(u.Path, "/"))
	if seg == "." || seg == "/" || !pacman.ValidName(seg) {
		return "", false
	}

	return seg, true
}

// checkArch returns group.ErrDistNotFound unless some member has packages for
// arch: a hosted member holds one filed under it, a proxy member's upstream has
// a database for it. A proxy member is asked once (the proxy caches its answer).
func (w *GroupMetadataWorker) checkArch(ctx context.Context, arch string, members []*models.Repository) error {
	if !pacman.ValidArch(arch) {
		return group.ErrDistNotFound
	}

	var firstErr error

	for _, m := range members {
		if m.RepoType == repotype.Proxy {
			section, ok := proxySection(m)
			if !ok {
				continue
			}

			rc, err := w.opener.Open(ctx, m, pacmanArchPath(arch, section+".db"))
			if err == nil {
				_ = rc.Close()
				return nil
			}

			if !errors.Is(err, proxy.ErrNotFound) {
				firstErr = cmpFirst(firstErr, err)
			}

			continue
		}

		pkgs, err := w.db.ListPacmanPackages(ctx, m.ID)
		if err != nil {
			return fmt.Errorf("group worker: querying packages of %s: %w", m.Name, err)
		}

		for _, p := range pkgs {
			if p.RepoArch == arch {
				return nil
			}
		}
	}

	if firstErr != nil {
		return firstErr
	}

	return group.ErrDistNotFound
}

// loadHostedPacman reads the packages of the hosted members.
func (j *groupJob) loadHostedPacman(ctx context.Context) (map[string]*pacmanHosted, error) {
	out := make(map[string]*pacmanHosted)

	for _, m := range j.members {
		if m.RepoType == repotype.Proxy {
			continue
		}

		pkgs, err := j.w.db.ListPacmanPackages(ctx, m.ID)
		if err != nil {
			return nil, fmt.Errorf("group worker: querying packages of %s: %w", m.Name, err)
		}

		lists, err := j.w.db.ListPacmanFileLists(ctx, m.ID)
		if err != nil {
			return nil, fmt.Errorf("group worker: querying file lists of %s: %w", m.Name, err)
		}

		out[m.ID] = &pacmanHosted{pkgs: pkgs, lists: lists}
	}

	return out, nil
}

// buildPacman generates the databases of every architecture the group serves:
// the ones it published before (known), the ones its hosted members have
// packages for and the one a client just asked for (hint). An architecture no
// member has packages for is not published; one whose proxy members could not
// be read is kept for the retry.
func (j *groupJob) buildPacman(ctx context.Context, known []string, hint string) error {
	hosted, err := j.loadHostedPacman(ctx)
	if err != nil {
		return err
	}

	archs := make(map[string]struct{})

	for _, a := range known {
		archs[a] = struct{}{}
	}

	if hint != "" {
		archs[hint] = struct{}{}
	}

	for _, h := range hosted {
		for _, p := range h.pkgs {
			archs[p.RepoArch] = struct{}{}
		}
	}

	ordered := make([]string, 0, len(archs))

	for a := range archs {
		if pacman.ValidArch(a) {
			ordered = append(ordered, a)
		}
	}

	sort.Strings(ordered)

	hintFound := hint == ""

	for _, arch := range ordered {
		found, err := j.buildPacmanArch(ctx, arch, hosted)
		if err != nil {
			return err
		}

		if found || (j.partial && slices.Contains(known, arch)) {
			j.dists = append(j.dists, arch)
		}

		if found && arch == hint {
			hintFound = true
		}
	}

	if !hintFound {
		if j.partial {
			return errPacmanMembersUnreadable
		}

		return group.ErrDistNotFound
	}

	j.addPublicKey()

	return nil
}

// buildPacmanArch renders the databases of one architecture from every
// member's. found is false when no member contributes a package.
func (j *groupJob) buildPacmanArch(ctx context.Context, arch string, hosted map[string]*pacmanHosted) (bool, error) {
	contribs := make([]pacmanContribution, 0, len(j.members))
	proxies := make(map[string]*models.Repository)

	for _, m := range j.members {
		if m.RepoType == repotype.Proxy {
			if entries, ok := j.readProxyPacmanDB(ctx, m, arch); ok {
				contribs = append(contribs, pacmanContribution{member: m.ID, proxy: m, entries: entries})
				proxies[m.ID] = m
			}

			continue
		}

		if h := hosted[m.ID]; h != nil {
			contribs = append(contribs, pacmanContribution{member: m.ID, entries: pacmanEntries(h.pkgs, h.lists, arch)})
		}
	}

	picks := j.claimPacman(arch, mergePacman(contribs, j.group.MergeProxyVersions))
	if len(picks) == 0 {
		return false, nil
	}

	j.fillProxyFileLists(ctx, arch, picks, proxies)

	entries := make([]pacman.DBEntry, len(picks))
	for i := range picks {
		entries[i] = picks[i].entry
	}

	if err := renderArchDatabases(j.blobs, arch, entries, j.signer); err != nil {
		return false, err
	}

	return true, nil
}

// claimPacman registers the owner of the package file of every pick and drops
// the ones whose file another member already owns for different content.
func (j *groupJob) claimPacman(arch string, picks []pacmanPick) []pacmanPick {
	kept := picks[:0]

	for _, p := range picks {
		if j.claim(pacmanArchPath(arch, p.entry.Filename), p.member, p.entry.SHA256) {
			kept = append(kept, p)
		}
	}

	return kept
}

// readProxyPacmanDB reads the database of a proxy member for arch. ok is false
// when the member has none: its upstream has no such architecture, no section
// name can be derived from its URL, or reading failed (the generation is then
// partial).
func (j *groupJob) readProxyPacmanDB(ctx context.Context, m *models.Repository, arch string) ([]pacman.DBEntry, bool) {
	section, ok := proxySection(m)
	if !ok {
		j.w.log.Warnw("group worker: proxy member has no repository name in its upstream URL, so it cannot supply a pacman database; "+
			"point it at the repository (for example https://mirror.example/extra)", "group", j.group.Name, "member", m.Name)

		return nil, false
	}

	rel := pacmanArchPath(arch, section+".db")

	rc, err := j.w.opener.Open(ctx, m, rel)
	if err != nil {
		j.noteFetchError(m, rel, err)
		return nil, false
	}

	defer func() { _ = rc.Close() }()

	entries, err := pacman.ReadDB(io.LimitReader(rc, maxUpstreamPacmanDB))
	if err != nil {
		j.noteFetchError(m, rel, err)
		return nil, false
	}

	return entries, true
}

// fillProxyFileLists adds the file lists of the picked proxy packages from the
// members' files databases. The lists are optional: a member without a files
// database, or one that cannot be read, contributes entries without a list.
func (j *groupJob) fillProxyFileLists(ctx context.Context, arch string, picks []pacmanPick, proxies map[string]*models.Repository) {
	for id, m := range proxies {
		want := make(map[string]struct{})

		for i := range picks {
			if picks[i].member == id {
				want[picks[i].entry.DirName()] = struct{}{}
			}
		}

		if len(want) == 0 {
			continue
		}

		lists, err := j.readProxyFileLists(ctx, m, arch, want)
		if err != nil {
			if !errors.Is(err, proxy.ErrNotFound) {
				j.w.log.Warnw("group worker: files database of a proxy member unreadable, listing its packages without files",
					"group", j.group.Name, "member", m.Name, "arch", arch, "error", err)
			}

			continue
		}

		for i := range picks {
			if picks[i].member == id {
				picks[i].entry.Files = lists[picks[i].entry.DirName()]
			}
		}
	}
}

func (j *groupJob) readProxyFileLists(
	ctx context.Context,
	m *models.Repository,
	arch string,
	want map[string]struct{},
) (map[string][]string, error) {
	section, _ := proxySection(m) // the database of this member was read, so it has one

	rc, err := j.w.opener.Open(ctx, m, pacmanArchPath(arch, section+".files"))
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller classifies the sentinel
	}

	defer func() { _ = rc.Close() }()

	lists, err := pacman.ReadFileLists(io.LimitReader(rc, maxUpstreamPacmanDB), func(dir string) bool {
		_, ok := want[dir]
		return ok
	})
	if err != nil {
		return nil, fmt.Errorf("reading files database: %w", err)
	}

	return lists, nil
}
