// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
)

// dockerPromoter promotes docker/OCI images.
//
// A bundle item is the digest of an image manifest (or index): its sha256 is the
// blobs.sha256 of the manifest bytes, which every docker_manifests row of the
// source repository references through blob_id. Each live source manifest with
// that content (one per image namespace holding it) is copied together with
// everything a client needs to pull it from the destination:
//
//   - the manifest row (subject_digest included) and its docker_manifest_refs;
//   - for an index, every live child manifest of the same image, recursively;
//   - the config and layer blobs: a docker_blobs row (same blobs row, new
//     repository) and the docker_blob_images link of the image, unless the
//     destination already holds the digest;
//   - the live source tags pointing at the item's manifest (not at index
//     children), unless the destination already has the tag name for the image:
//     on the same digest it is reported as existing, on another digest it is a
//     conflict (tags are mutable in a registry, a promotion never moves one).
//
// A manifest the destination already holds is Existing (a digest is content);
// its blobs and children are still completed, so a partial destination ends up
// installable. Quota follows the registry: layers and configs are bytes only
// per docker_blobs row created, a manifest is its size and one artifact.
type dockerPromoter struct{}

func (dockerPromoter) Format() string { return "docker" }

// ErrDockerNotIndexed: a source manifest whose references registry GC has not
// recorded yet (pushed before docker_manifest_refs existed or restored from a
// backup). Promotion cannot know its layers without reading the manifest body.
var ErrDockerNotIndexed = errors.New("promote: docker manifest references are not indexed yet (registry GC records them), retry later")

// ErrDockerTagChanged: a destination tag vanished while it was being checked.
var ErrDockerTagChanged = errors.New("promote: docker tag changed concurrently in the target repository")

type dockerTag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// dockerManifest is a live manifest row of the source repository.
type dockerManifest struct {
	id     string
	image  string
	digest string
	sha    string
	size   int64
	tags   []dockerTag
}

// dockerBlob is a source docker_blobs row a manifest references.
type dockerBlob struct {
	id      string
	digest  string
	size    int64
	created bool
}

// dockerRun is one Promote call: its transaction, request, result and the
// source manifests and quarantine keys already handled.
type dockerRun struct {
	tx   Tx
	req  Request
	res  *Result
	seen map[string]struct{}
	keys map[string]struct{}
}

// Promote implements Promoter.
func (dockerPromoter) Promote(ctx context.Context, tx Tx, req Request) (*Result, error) {
	if err := lockDockerRepos(ctx, tx, req); err != nil {
		return nil, err
	}

	roots, err := dockerRoots(ctx, tx, req)
	if err != nil {
		return nil, err
	}

	run := &dockerRun{
		tx:   tx,
		req:  req,
		res:  &Result{},
		seen: map[string]struct{}{},
		keys: map[string]struct{}{},
	}

	for _, root := range roots {
		if err := run.promoteRoot(ctx, root); err != nil {
			return nil, err
		}
	}

	return run.res, nil
}

// lockDockerRepos takes the shared docker lock of both repositories, in id
// order so that concurrent promotions between the same pair cannot deadlock
// against a registry GC waiting for its exclusive lock.
func lockDockerRepos(ctx context.Context, tx Tx, req Request) error {
	ids := []string{req.Src.ID, req.Dst.ID}
	slices.Sort(ids)

	for _, id := range slices.Compact(ids) {
		if _, err := tx.ExecContext(ctx, dockerLockSQL, dockerRepoLockPrefix+id); err != nil {
			return fmt.Errorf("lock docker repository: %w", err)
		}
	}

	return nil
}

// dockerName is the distribution name of an image: the repository name plus the
// image path inside it (nothing for the repository's default image).
func dockerName(repoName, image string) string {
	if image == "" {
		return repoName
	}

	return repoName + "/" + image
}

// dockerRoots returns the live source manifests holding the item's content,
// narrowed to the one Request.Path names when it names one of them.
func dockerRoots(ctx context.Context, tx Tx, req Request) ([]dockerManifest, error) {
	rows, err := tx.QueryContext(ctx, dockerRootsSQL, req.Src.ID, req.SHA256)
	if err != nil {
		return nil, fmt.Errorf("select docker source manifests: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var roots []dockerManifest

	for rows.Next() {
		var (
			m        dockerManifest
			tagsJSON []byte
		)

		if err := rows.Scan(&m.id, &m.image, &m.digest, &m.sha, &m.size, &tagsJSON); err != nil {
			return nil, fmt.Errorf("scan docker source manifest: %w", err)
		}

		if err := json.Unmarshal(tagsJSON, &m.tags); err != nil {
			return nil, fmt.Errorf("decode docker tags: %w", err)
		}

		roots = append(roots, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read docker source manifests: %w", err)
	}

	if len(roots) == 0 {
		return nil, ErrItemNotFound
	}

	return narrowDockerRoots(roots, req), nil
}

// narrowDockerRoots keeps the manifests whose identifiers equal req.Path; with
// no such manifest, or no path, every manifest stays.
func narrowDockerRoots(roots []dockerManifest, req Request) []dockerManifest {
	if len(roots) < 2 || strings.TrimPrefix(req.Path, "/") == "" {
		return roots
	}

	cands := make([]candidate, 0, len(roots))
	for _, m := range roots {
		cands = append(cands, candidate{id: m.id, idents: m.idents(req.Src.Name)})
	}

	kept := make([]dockerManifest, 0, len(roots))

	for _, c := range narrow(cands, req.Path) {
		for _, m := range roots {
			if m.id == c.id {
				kept = append(kept, m)

				break
			}
		}
	}

	return kept
}

// idents are the spellings of the manifest an item path may use: row id,
// digest (with and without algorithm), image, distribution name, and each of
// those combined with the tags.
func (m dockerManifest) idents(repoName string) []string {
	name := dockerName(repoName, m.image)
	ids := []string{m.id, m.digest, m.sha, name, name + "@" + m.digest, name + "/manifests/" + m.digest}

	if m.image != "" {
		ids = append(ids, m.image)
	}

	for _, t := range m.tags {
		ids = append(ids, t.Name, name+":"+t.Name, name+"/manifests/"+t.Name)

		if m.image != "" {
			ids = append(ids, m.image+":"+t.Name)
		}
	}

	return ids
}

// quarantineKeys are the identifiers the registry's manifest pull checks for
// quarantine: the manifest row id, its digest and the manifest paths, plus the
// image and, for a promoted item, its tags (row ids, names and tag paths).
func (m dockerManifest) quarantineKeys(repoName string, withTags bool) []string {
	name := dockerName(repoName, m.image)
	keys := []string{m.id, m.digest, m.sha, name, name + "/manifests/" + m.digest}

	if m.image != "" {
		keys = append(keys, m.image)
	}

	if withTags {
		for _, t := range m.tags {
			keys = append(keys, t.ID, t.Name, name+"/manifests/"+t.Name)
		}
	}

	return keys
}

// addKeys appends quarantine keys not recorded yet.
func (r *dockerRun) addKeys(keys ...string) {
	for _, k := range keys {
		if _, dup := r.keys[k]; dup {
			continue
		}

		r.keys[k] = struct{}{}
		r.res.QuarantineKeys = append(r.res.QuarantineKeys, k)
	}
}

// promoteRoot copies one item manifest: its tags first (so a tag conflict fails
// before any data is copied), then the manifest with its blobs and children.
func (r *dockerRun) promoteRoot(ctx context.Context, root dockerManifest) error {
	r.addKeys(root.quarantineKeys(r.req.Src.Name, true)...)

	tagged, err := r.copyTags(ctx, root)
	if err != nil {
		return err
	}

	version := ""
	if len(root.tags) > 0 {
		version = root.tags[0].Name
	}

	return r.copyManifest(ctx, root, version, tagged > 0)
}

// copyTags copies the live source tags of the item manifest and returns how
// many were created in the destination.
func (r *dockerRun) copyTags(ctx context.Context, root dockerManifest) (int, error) {
	created := 0

	for _, t := range root.tags {
		ok, err := r.copyTag(ctx, root, t)
		if err != nil {
			return 0, err
		}

		if ok {
			created++
		}
	}

	return created, nil
}

// copyTag copies one tag. It reports false when the destination already has the
// tag on the same manifest digest. A tag on another digest is a conflict, or,
// when forced, is moved to the trash and replaced by the source tag.
func (r *dockerRun) copyTag(ctx context.Context, root dockerManifest, t dockerTag) (bool, error) {
	name := dockerName(r.req.Dst.Name, root.image)

	created, err := r.insertTag(ctx, name, t)
	if err != nil || created {
		return created, err
	}

	var (
		id, digest string
		old        destRow
	)

	err = r.tx.QueryRowContext(ctx, dockerTagDigestSQL, r.req.Dst.ID, root.image, t.Name).Scan(&id, &digest, &old.sha, &old.size)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("docker tag %s:%s: %w", name, t.Name, ErrDockerTagChanged)
	case err != nil:
		return false, fmt.Errorf("look up docker tag %s:%s in the target: %w", name, t.Name, err)
	case digest == root.digest:
		return false, nil
	case !r.req.Force:
		return false, r.req.conflict("docker", name+":"+t.Name, t.Name)
	}

	if err := r.req.displace(ctx, r.tx, models.TrashKindDockerTag, id); err != nil {
		return false, err
	}

	r.res.Replaced = append(r.res.Replaced, Artifact{
		Name:    name,
		Version: t.Name,
		Path:    name + "/manifests/" + digest,
		SHA256:  old.sha,
		Size:    old.size,
	})

	created, err = r.insertTag(ctx, name, t)
	if err != nil {
		return false, err
	}

	if !created {
		return false, fmt.Errorf("docker tag %s:%s: %w", name, t.Name, ErrDockerTagChanged)
	}

	return true, nil
}

// insertTag copies source tag t into the destination and reports whether the
// row was created (false: the destination already holds a live tag of the name).
func (r *dockerRun) insertTag(ctx context.Context, name string, t dockerTag) (bool, error) {
	var id string

	err := r.tx.QueryRowContext(ctx, dockerInsertTagSQL, r.req.Dst.ID, t.ID).Scan(&id)

	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	}

	return false, fmt.Errorf("copy docker tag %s:%s: %w", name, t.Name, err)
}

// copyManifest copies manifest m (once per Promote call) with its blobs and, for
// an index, its children. changed marks a manifest whose destination row
// already existed but that the promotion still changed (it gained tags).
func (r *dockerRun) copyManifest(ctx context.Context, m dockerManifest, version string, changed bool) error {
	if _, done := r.seen[m.id]; done {
		return nil
	}

	r.seen[m.id] = struct{}{}

	if err := r.requireIndexed(ctx, m); err != nil {
		return err
	}

	created, err := r.insertManifest(ctx, m)
	if err != nil {
		return err
	}

	r.addKeys(m.quarantineKeys(r.req.Src.Name, false)...)

	if err := r.copyBlobs(ctx, m); err != nil {
		return err
	}

	r.account(m, version, created, changed)

	children, err := r.children(ctx, m)
	if err != nil {
		return err
	}

	for _, child := range children {
		if err := r.copyManifest(ctx, child, "", false); err != nil {
			return err
		}
	}

	return nil
}

// account records the outcome for manifest m: Copied (and charged: its size and
// one artifact, like a manifest push) when its row was created or the promotion
// changed it, Existing otherwise.
func (r *dockerRun) account(m dockerManifest, version string, created, changed bool) {
	name := dockerName(r.req.Dst.Name, m.image)
	art := Artifact{
		Name:    name,
		Version: version,
		Path:    name + "/manifests/" + m.digest,
		SHA256:  m.sha,
		Size:    m.size,
	}

	if created || changed {
		r.res.Copied = append(r.res.Copied, art)
	} else {
		r.res.Existing = append(r.res.Existing, art)
	}

	if created {
		r.res.Usage.Bytes += m.size
		r.res.Usage.Artifacts++
	}
}

// requireIndexed fails for a source manifest whose references are not recorded.
func (r *dockerRun) requireIndexed(ctx context.Context, m dockerManifest) error {
	var indexed bool

	if err := r.tx.QueryRowContext(ctx, dockerIndexedSQL, m.id).Scan(&indexed); err != nil {
		return fmt.Errorf("check docker manifest %s references: %w", m.digest, err)
	}

	if !indexed {
		return fmt.Errorf("docker manifest %s: %w", m.digest, ErrDockerNotIndexed)
	}

	return nil
}

// insertManifest copies the manifest row and its references into the
// destination. It reports false when the destination already held the manifest.
func (r *dockerRun) insertManifest(ctx context.Context, m dockerManifest) (bool, error) {
	var newID string

	err := r.tx.QueryRowContext(ctx, dockerInsertManifestSQL, r.req.Dst.ID, m.id).Scan(&newID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("copy docker manifest %s: %w", m.digest, err)
	}

	if _, err := r.tx.ExecContext(ctx, dockerInsertRefsSQL, newID, r.req.Dst.ID, m.id); err != nil {
		return false, fmt.Errorf("copy docker manifest %s references: %w", m.digest, err)
	}

	return true, nil
}

// copyBlobs copies the config/layer blobs manifest m references into the
// destination, charges the bytes of the rows it created (no artifact, as in the
// registry) and links them to the manifest's image.
func (r *dockerRun) copyBlobs(ctx context.Context, m dockerManifest) error {
	blobs, err := r.blobs(ctx, m)
	if err != nil {
		return err
	}

	name := dockerName(r.req.Src.Name, m.image)

	for _, b := range blobs {
		r.addKeys(b.id, b.digest, name+"/blobs/"+b.digest)

		if b.created {
			r.res.Usage.Bytes += b.size
		}
	}

	if _, err := r.tx.ExecContext(ctx, dockerLinkBlobsSQL, m.id, r.req.Dst.ID, m.image); err != nil {
		return fmt.Errorf("link docker blobs of %s to the image: %w", m.digest, err)
	}

	return nil
}

// blobs runs the blob copy of manifest m and returns the source blob rows.
func (r *dockerRun) blobs(ctx context.Context, m dockerManifest) ([]dockerBlob, error) {
	rows, err := r.tx.QueryContext(ctx, dockerCopyBlobsSQL, m.id, r.req.Dst.ID)
	if err != nil {
		return nil, fmt.Errorf("copy docker blobs of %s: %w", m.digest, err)
	}

	defer func() { _ = rows.Close() }()

	var blobs []dockerBlob

	for rows.Next() {
		var b dockerBlob

		if err := rows.Scan(&b.id, &b.digest, &b.size, &b.created); err != nil {
			return nil, fmt.Errorf("scan docker blob of %s: %w", m.digest, err)
		}

		blobs = append(blobs, b)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read docker blobs of %s: %w", m.digest, err)
	}

	return blobs, nil
}

// children returns the live child manifests of an index manifest. A child the
// source does not hold (never pushed, or trashed) is skipped: the source index
// dangles on it just the same.
func (r *dockerRun) children(ctx context.Context, m dockerManifest) ([]dockerManifest, error) {
	rows, err := r.tx.QueryContext(ctx, dockerChildrenSQL, m.id)
	if err != nil {
		return nil, fmt.Errorf("select children of docker manifest %s: %w", m.digest, err)
	}

	defer func() { _ = rows.Close() }()

	var children []dockerManifest

	for rows.Next() {
		var c dockerManifest

		if err := rows.Scan(&c.id, &c.image, &c.digest, &c.sha, &c.size); err != nil {
			return nil, fmt.Errorf("scan child of docker manifest %s: %w", m.digest, err)
		}

		children = append(children, c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read children of docker manifest %s: %w", m.digest, err)
	}

	return children, nil
}
