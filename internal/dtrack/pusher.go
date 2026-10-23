// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package dtrack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/sbom"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	queueSize        = 256
	defaultWorkers   = 2
	defaultPollEvery = 2 * time.Second
	defaultMaxWait   = 5 * time.Minute
)

// Sources builds the documents a push sends, from an artifact's stored state.
type Sources interface {
	// BOM returns the artifact's CycloneDX SBOM (without vulnerabilities:
	// Dependency-Track runs its own analysis) and its component count.
	BOM(ctx context.Context, repo *models.Repository, artifactID string) (doc []byte, components int, err error)
	// VEX returns the artifact's CycloneDX VEX shaped by opts and its statement count.
	VEX(ctx context.Context, repo *models.Repository, artifactID string, opts sbom.VEXOptions) (doc []byte, statements int, err error)
}

// Store is the database part the pusher needs.
type Store interface {
	SettingsStore
	GetRepositoryByID(ctx context.Context, id string) (*models.Repository, error)
	UpsertDependencyTrackPush(ctx context.Context, s *models.DependencyTrackPush) error
	GetDependencyTrackPush(ctx context.Context, repoID, artifactID string) (*models.DependencyTrackPush, error)
}

// Trigger says what caused a push.
type Trigger string

// Push triggers.
const (
	TriggerUpload Trigger = "upload"
	TriggerScan   Trigger = "scan"
	TriggerManual Trigger = "manual"
)

// Job is one artifact to push.
type Job struct {
	RepoID     string
	ArtifactID string
	Trigger    Trigger
}

func (j Job) key() string { return j.RepoID + "\x00" + j.ArtifactID }

// ClientFactory builds the Dependency-Track client of a configuration (tests inject a plain one).
type ClientFactory func(baseURL, apiKey string) (*Client, error)

// Pusher pushes BOMs and VEX statements in the background. Jobs are
// de-duplicated per artifact: one waiting job absorbs further requests, and a
// request arriving while the artifact is being pushed runs again afterwards.
type Pusher struct {
	db      Store
	src     Sources
	log     *logger.Logger
	newCl   ClientFactory
	poll    time.Duration
	maxWait time.Duration

	jobs chan Job

	mu      sync.Mutex
	waiting map[string]struct{}
	running map[string]*Job // value non-nil: rerun requested

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// NewPusher creates a stopped pusher; Start launches its workers.
func NewPusher(db Store, src Sources, log *logger.Logger) *Pusher {
	return &Pusher{
		db: db, src: src, log: log, newCl: NewClient,
		poll: defaultPollEvery, maxWait: defaultMaxWait,
		jobs:    make(chan Job, queueSize),
		waiting: map[string]struct{}{}, running: map[string]*Job{},
	}
}

// WithTimings overrides how often and for how long the processing token is polled.
func (p *Pusher) WithTimings(poll, maxWait time.Duration) *Pusher {
	p.poll, p.maxWait = poll, maxWait
	return p
}

// WithClientFactory replaces the Dependency-Track client constructor.
func (p *Pusher) WithClientFactory(f ClientFactory) *Pusher {
	p.newCl = f
	return p
}

// Start launches the workers; they run until Stop or ctx is done.
func (p *Pusher) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)

	for range defaultWorkers {
		p.wg.Go(func() { p.work(ctx) })
	}
}

// Stop cancels the workers and waits for them.
func (p *Pusher) Stop() {
	if p.cancel != nil {
		p.cancel()
	}

	p.wg.Wait()
}

func (p *Pusher) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-p.jobs:
			p.finish(ctx, j)
		}
	}
}

// finish runs j, then reruns it once more when it was requested meanwhile.
func (p *Pusher) finish(ctx context.Context, j Job) {
	k := j.key()

	p.mu.Lock()
	delete(p.waiting, k)
	p.running[k] = nil
	p.mu.Unlock()

	p.process(ctx, j)

	p.mu.Lock()
	again := p.running[k]
	delete(p.running, k)
	p.mu.Unlock()

	if again != nil {
		p.Enqueue(*again)
	}
}

// Enqueue schedules a push without checking the configuration (use
// EnqueueIfEnabled for events). It reports false when the queue is full.
func (p *Pusher) Enqueue(j Job) bool {
	k := j.key()

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.waiting[k]; ok {
		return true
	}

	if _, ok := p.running[k]; ok {
		again := j
		p.running[k] = &again

		return true
	}

	select {
	case p.jobs <- j:
		p.waiting[k] = struct{}{}
		return true
	default:
		return false
	}
}

// EnqueueIfEnabled schedules a push when the repository's effective
// configuration is ready and has auto-push on; it is what upload and scan
// events call. Configuration errors are logged, never returned: an event must
// not fail because of the integration.
func (p *Pusher) EnqueueIfEnabled(ctx context.Context, j Job) {
	eff, err := EffectiveFor(ctx, p.db, j.RepoID)
	if err != nil {
		p.log.Warnw("dependency-track: reading configuration failed", "repository_id", j.RepoID, "error", err)
		return
	}

	if !eff.Ready() || !eff.AutoPush {
		return
	}

	if !p.Enqueue(j) {
		p.log.Warnw("dependency-track: push queue full, dropping", "repository_id", j.RepoID, "artifact", j.ArtifactID)
	}
}

// ---- one push ------------------------------------------------------------

// skipError aborts a push that has nothing to do; its message is the detail.
type skipError string

func (e skipError) Error() string { return string(e) }

func (p *Pusher) process(ctx context.Context, j Job) {
	rec, err := p.begin(ctx, j)
	if err != nil {
		p.log.Warnw("dependency-track: recording push start failed", "repository_id", j.RepoID, "artifact", j.ArtifactID, "error", err)
		return
	}

	err = p.run(ctx, j, rec)

	switch {
	case err == nil:
		rec.State = models.DTrackPushDone
	case errors.As(err, new(skipError)):
		rec.State, rec.Detail = models.DTrackPushSkipped, err.Error()
	default:
		p.log.Warnw("dependency-track: push failed", "repository_id", j.RepoID, "artifact", j.ArtifactID, "error", err)
		rec.State, rec.Detail = models.DTrackPushFailed, failureDetail(err)
	}

	// The run's context may be the reason it failed (shutdown): record regardless.
	if err := p.db.UpsertDependencyTrackPush(context.WithoutCancel(ctx), rec); err != nil {
		p.log.Warnw("dependency-track: recording push result failed", "repository_id", j.RepoID, "artifact", j.ArtifactID, "error", err)
	}
}

// failureDetail is the operator-facing reason of a failed push: it never
// carries response bodies or URLs, only the kind of failure.
func failureDetail(err error) string {
	var he *HTTPError

	switch {
	case errors.As(err, &he):
		switch he.Status {
		case 401, 403:
			return fmt.Sprintf("dependency-track rejected the API key or it lacks permissions (%s: HTTP %d)", he.Op, he.Status)
		default:
			return fmt.Sprintf("dependency-track %s failed with HTTP %d", he.Op, he.Status)
		}
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out waiting for dependency-track"
	case errors.Is(err, ErrNotFound):
		return "dependency-track project not found after upload"
	case errors.Is(err, ErrInvalidURL):
		return err.Error()
	default:
		return "dependency-track push failed; see the server log"
	}
}

func (p *Pusher) begin(ctx context.Context, j Job) (*models.DependencyTrackPush, error) {
	rec, err := p.db.GetDependencyTrackPush(ctx, j.RepoID, j.ArtifactID)
	if err != nil {
		return nil, fmt.Errorf("read previous push: %w", err)
	}

	if rec == nil {
		rec = &models.DependencyTrackPush{RepositoryID: j.RepoID, ArtifactID: j.ArtifactID}
	}

	rec.State, rec.Detail = models.DTrackPushRunning, ""

	if err := p.db.UpsertDependencyTrackPush(ctx, rec); err != nil {
		return nil, fmt.Errorf("record push start: %w", err)
	}

	return rec, nil
}

func (p *Pusher) run(ctx context.Context, j Job, rec *models.DependencyTrackPush) error {
	repo, err := p.db.GetRepositoryByID(ctx, j.RepoID)
	if err != nil {
		return fmt.Errorf("read repository: %w", err)
	}

	if repo == nil {
		return skipError("repository no longer exists")
	}

	eff, err := EffectiveFor(ctx, p.db, j.RepoID)
	if err != nil {
		return err
	}

	if !eff.Ready() {
		return skipError("Dependency-Track is not enabled or not fully configured for this repository")
	}

	cl, err := p.newCl(eff.BaseURL, eff.APIKey)
	if err != nil {
		return err
	}

	name, version := artifactNameVersion(j.ArtifactID)
	vars := ProjectVars{Repo: repo.Name, Name: name, Version: version, ArtifactID: j.ArtifactID, Format: repo.Format}
	rec.ProjectName = Render(eff.ProjectName, DefaultProjectName, vars)
	rec.ProjectVersion = Render(eff.ProjectVersion, DefaultProjectVersion, vars)

	if rec.ProjectName == "" {
		return skipError("the project name template renders empty for this artifact")
	}

	if err := p.pushBOM(ctx, cl, repo, j, rec); err != nil {
		return err
	}

	if !eff.PushVEX {
		return nil
	}

	return p.pushVEX(ctx, cl, repo, j, rec)
}

func artifactNameVersion(artifactID string) (string, string) {
	if i := strings.LastIndex(artifactID, "@"); i > 0 {
		return artifactID[:i], artifactID[i+1:]
	}

	return artifactID, ""
}

func (p *Pusher) pushBOM(ctx context.Context, cl *Client, repo *models.Repository, j Job, rec *models.DependencyTrackPush) error {
	bom, components, err := p.src.BOM(ctx, repo, j.ArtifactID)
	if err != nil {
		return fmt.Errorf("build BOM: %w", err)
	}

	token, err := cl.UploadBOM(ctx, rec.ProjectName, rec.ProjectVersion, bom)
	if err != nil {
		return err
	}

	if err := p.await(ctx, cl, token); err != nil {
		return err
	}

	now := time.Now()
	rec.Components, rec.BOMPushedAt = components, &now

	uuid, err := cl.ProjectUUID(ctx, rec.ProjectName, rec.ProjectVersion)
	if err != nil {
		return err
	}

	rec.ProjectUUID = uuid

	return nil
}

// pushVEX sends the admin's VEX decisions for the vulnerabilities
// Dependency-Track itself found in the project, and counts the analyses it applied.
func (p *Pusher) pushVEX(ctx context.Context, cl *Client, repo *models.Repository, j Job, rec *models.DependencyTrackPush) error {
	findings, err := cl.Findings(ctx, rec.ProjectUUID)
	if err != nil {
		return err
	}

	known := newVulnIndex(findings)

	doc, statements, err := p.src.VEX(ctx, repo, j.ArtifactID, sbom.VEXOptions{DecidedOnly: true, ResolveVuln: known.resolve})
	if err != nil {
		return fmt.Errorf("build VEX: %w", err)
	}

	rec.VEXStatements = statements

	if statements == 0 {
		rec.Detail = "BOM pushed; no VEX decision matches a vulnerability Dependency-Track reports"
		return nil
	}

	token, err := cl.UploadVEX(ctx, rec.ProjectUUID, doc)
	if err != nil {
		return err
	}

	if err := p.await(ctx, cl, token); err != nil {
		return err
	}

	now := time.Now()
	rec.VEXPushedAt = &now

	after, err := cl.Findings(ctx, rec.ProjectUUID)
	if err != nil {
		return err
	}

	rec.VEXApplied = known.applied(after)

	return nil
}

// await polls the processing token until Dependency-Track is done with it.
func (p *Pusher) await(ctx context.Context, cl *Client, token string) error {
	ctx, cancel := context.WithTimeout(ctx, p.maxWait)
	defer cancel()

	t := time.NewTicker(p.poll)
	defer t.Stop()

	for {
		busy, err := cl.Processing(ctx, token)
		if err != nil {
			return err
		}

		if !busy {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for processing: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// ---- vulnerability identity mapping ---------------------------------------

// vulnIndex maps every identifier of the project's Dependency-Track findings
// (its own id and its aliases, upper-cased) to the id and source DT knows it by.
// DT only applies a VEX statement whose vulnerability source and id match its own.
type vulnIndex struct {
	byID map[string]sbom.VulnRef
}

func newVulnIndex(findings []Finding) *vulnIndex {
	idx := &vulnIndex{byID: map[string]sbom.VulnRef{}}

	for _, f := range findings {
		ref := sbom.VulnRef{ID: f.VulnID, Source: f.Source}
		idx.byID[strings.ToUpper(f.VulnID)] = ref

		for _, a := range f.Aliases {
			if _, taken := idx.byID[strings.ToUpper(a)]; !taken {
				idx.byID[strings.ToUpper(a)] = ref
			}
		}
	}

	return idx
}

func (x *vulnIndex) resolve(id string) (sbom.VulnRef, bool) {
	r, ok := x.byID[strings.ToUpper(id)]
	return r, ok
}

// applied counts the distinct (vulnerability, component) findings that carry an
// analysis after the VEX import, among the vulnerabilities the VEX named.
func (x *vulnIndex) applied(findings []Finding) int {
	n := 0

	for _, f := range findings {
		if f.State == "" || f.State == "NOT_SET" {
			continue
		}

		if _, ok := x.byID[strings.ToUpper(f.VulnID)]; ok {
			n++
		}
	}

	return n
}
