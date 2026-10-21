// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // only compared against the source system's SHA-1 when it offers nothing stronger
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	errImportUsage  = errors.New("invalid arguments")
	errImportVerify = errors.New("verification failed")
)

const (
	importUsage = "Usage: oa import <nexus|artifactory> --source-url URL --repo SRC[:DST] [--repo ...] [flags]"

	envSourceURL   = "OA_IMPORT_SOURCE_URL"
	envSourceUser  = "OA_IMPORT_SOURCE_USER"
	envSourceToken = "OA_IMPORT_SOURCE_TOKEN"

	defaultImportConcurrency = 4
	futureSkew               = time.Minute
)

// repoMapping routes one source repository to one destination repository.
type repoMapping struct {
	Src string
	Dst string
}

// importConfig is the parsed `oa import` invocation.
type importConfig struct {
	Kind        string
	SourceURL   *url.URL
	SourceUser  string
	SourceToken string
	Repos       []repoMapping
	DryRun      bool
	Concurrency int
	StateFile   string
	CreateRepos bool
	DebDist     string
	DebComp     string
	Properties  []string
	Listing     string
	Quiet       bool
	PathPrefix  string
}

type repoFlag []repoMapping

func (r *repoFlag) String() string { return fmt.Sprint([]repoMapping(*r)) }

func (r *repoFlag) Set(v string) error {
	src, dst, _ := strings.Cut(v, ":")
	if src == "" {
		return fmt.Errorf("%w: --repo %q (want SRC or SRC:DST)", errImportUsage, v)
	}

	if dst == "" {
		dst = src
	}

	*r = append(*r, repoMapping{Src: src, Dst: dst})

	return nil
}

// parseImportFlags parses the arguments after `oa import`. Source secrets come
// from the environment or a file, never from argv.
func parseImportFlags(args []string, getenv func(string) string, stdin io.Reader) (importConfig, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return importConfig{}, fmt.Errorf("%w: %s", errImportUsage, importUsage)
	}

	cfg := importConfig{Kind: args[0]}
	if cfg.Kind != "nexus" && cfg.Kind != "artifactory" {
		return importConfig{}, fmt.Errorf("%w: unknown source type %q (want nexus or artifactory)", errImportUsage, cfg.Kind)
	}

	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		repos     repoFlag
		sourceURL = fs.String("source-url", getenv(envSourceURL), "base URL of the source system")
		user      = fs.String("source-user", getenv(envSourceUser), "source user name (or "+envSourceUser+")")
		tokenFile = fs.String("source-token-file", "", "file holding the source token/password, '-' for stdin (or "+envSourceToken+")")
		props     = fs.String("properties", defaultPropertyPatterns, "properties to copy: comma-separated keys or prefix* patterns, 'none' to disable")
	)

	fs.Var(&repos, "repo", "repository to import as SRC or SRC:DST (repeatable)")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print the plan without writing anything")
	fs.IntVar(&cfg.Concurrency, "concurrency", defaultImportConcurrency, "parallel transfers")
	fs.StringVar(&cfg.StateFile, "state-file", "", "resume file: completed assets are skipped on the next run")
	fs.BoolVar(&cfg.CreateRepos, "create-repos", false, "create missing destination repositories (hosted, matching format)")
	fs.StringVar(&cfg.DebDist, "deb-distribution", "", "fallback Debian distribution when the source does not say")
	fs.StringVar(&cfg.DebComp, "deb-component", "", "fallback Debian component when the source does not say")
	fs.StringVar(&cfg.Listing, "artifactory-listing", listingAQL, "Artifactory listing mode: aql or storage")
	fs.StringVar(&cfg.PathPrefix, "path-prefix", "", "only import assets whose repository path starts with this prefix (e.g. com/acme/)")
	fs.BoolVar(&cfg.Quiet, "quiet", false, "only print the summary")

	if err := fs.Parse(args[1:]); err != nil {
		return importConfig{}, err
	}

	if fs.NArg() > 0 {
		return importConfig{}, fmt.Errorf("%w: unexpected argument %q", errImportUsage, fs.Arg(0))
	}

	cfg.Repos = repos
	cfg.SourceUser = *user
	cfg.Properties = parsePropertyPatterns(*props)

	if err := cfg.resolveSource(*sourceURL, *tokenFile, getenv, stdin); err != nil {
		return importConfig{}, err
	}

	return cfg, cfg.validate()
}

func (c *importConfig) validate() error {
	switch {
	case len(c.Repos) == 0:
		return fmt.Errorf("%w: at least one --repo is required", errImportUsage)
	case c.Concurrency < 1:
		return fmt.Errorf("%w: --concurrency must be at least 1", errImportUsage)
	case c.Listing != listingAQL && c.Listing != listingStorage:
		return fmt.Errorf("%w: --artifactory-listing must be aql or storage", errImportUsage)
	}

	return nil
}

func (c *importConfig) resolveSource(rawURL, tokenFile string, getenv func(string) string, stdin io.Reader) error {
	if rawURL == "" {
		return fmt.Errorf("%w: --source-url is required (or %s)", errImportUsage, envSourceURL)
	}

	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: invalid --source-url %q", errImportUsage, redactURL(rawURL))
	}

	if u.User != nil {
		return fmt.Errorf("%w: --source-url must not embed credentials; use --source-user and --source-token-file", errImportUsage)
	}

	c.SourceURL = u

	token, err := readSourceToken(tokenFile, getenv, stdin)
	if err != nil {
		return err
	}

	c.SourceToken = token

	if (c.SourceUser != "" || token != "") && u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("%w: refusing to send source credentials over plain HTTP; use an https --source-url", errImportUsage)
	}

	return nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}

	ip := net.ParseIP(h)

	return ip != nil && ip.IsLoopback()
}

func readSourceToken(file string, getenv func(string) string, stdin io.Reader) (string, error) {
	switch file {
	case "":
		return strings.TrimSpace(getenv(envSourceToken)), nil
	case "-":
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("reading source token from stdin: %w", err)
		}

		return strings.TrimSpace(line), nil
	}

	raw, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("reading source token file: %w", err)
	}

	return strings.TrimSpace(string(raw)), nil
}

func newImportSource(cfg importConfig, hc *http.Client) importSource {
	h := newSourceHTTP(cfg.SourceURL, cfg.SourceUser, cfg.SourceToken, hc)
	if cfg.Kind == "artifactory" {
		return &artifactorySource{http: h, listing: cfg.Listing}
	}

	return &nexusSource{http: h}
}

// handleImport implements `oa import` and returns the process exit code.
func handleImport(args []string, client *Client, outFmt string) int {
	cfg, err := parseImportFlags(args, os.Getenv, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}

	if !cfg.DryRun && client.token == "" {
		fmt.Fprintln(os.Stderr, "Error: not logged in; run `oa auth login` or set OA_TOKEN (an admin token is required)")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	progress := io.Writer(os.Stderr)
	if cfg.Quiet {
		progress = io.Discard
	}

	imp, err := newImporter(cfg, newImportSource(cfg, newHTTPClient()), &importTarget{c: client}, progress)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 2
	}

	defer imp.state.close()

	rep := imp.run(ctx)

	if outFmt == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep.snapshot()); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
	} else {
		rep.print(os.Stdout)
	}

	if rep.failed() {
		return 1
	}

	return 0
}

// ─── Report ───────────────────────────────────────────────────────────────────

type importFailure struct {
	Repo  string `json:"repo"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error"`
}

// importReport accumulates the outcome of a run. In a dry run "uploaded" and
// "bytes" are what would be transferred.
type importReport struct {
	mu          sync.Mutex
	progress    io.Writer
	DryRun      bool            `json:"dry_run"`
	Uploaded    int             `json:"uploaded"`
	Bytes       int64           `json:"bytes"`
	Present     int             `json:"skipped_present"`
	Resumed     int             `json:"skipped_state"`
	Ignored     int             `json:"ignored"`
	Failed      int             `json:"failed"`
	Interrupted bool            `json:"interrupted"`
	Failures    []importFailure `json:"failures"`
}

func (r *importReport) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, _ = fmt.Fprintf(r.progress, format+"\n", args...)
}

func (r *importReport) add(f func(*importReport)) {
	r.mu.Lock()
	defer r.mu.Unlock()

	f(r)
}

func (r *importReport) fail(repo, path string, err error) {
	r.add(func(r *importReport) {
		r.Failed++
		r.Failures = append(r.Failures, importFailure{Repo: repo, Path: path, Error: err.Error()})
	})
	r.logf("FAILED   %s/%s: %v", repo, path, err)
}

func (r *importReport) failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.Failed > 0 || r.Interrupted
}

func (r *importReport) snapshot() *importReport {
	r.mu.Lock()
	defer r.mu.Unlock()

	cp := &importReport{
		DryRun: r.DryRun, Uploaded: r.Uploaded, Bytes: r.Bytes, Present: r.Present,
		Resumed: r.Resumed, Ignored: r.Ignored, Failed: r.Failed, Interrupted: r.Interrupted,
		Failures: append([]importFailure{}, r.Failures...),
	}

	return cp
}

func (r *importReport) print(w io.Writer) {
	s := r.snapshot()

	verb := "uploaded"
	if s.DryRun {
		verb = "would upload"
	}

	_, _ = fmt.Fprintf(w, "\nImport summary%s\n", map[bool]string{true: " (dry run, nothing written)", false: ""}[s.DryRun])
	_, _ = fmt.Fprintf(w, "  %-22s %d (%s)\n", verb+":", s.Uploaded, humanBytes(s.Bytes))
	_, _ = fmt.Fprintf(w, "  %-22s %d\n", "already present:", s.Present)
	_, _ = fmt.Fprintf(w, "  %-22s %d\n", "resumed from state:", s.Resumed)
	_, _ = fmt.Fprintf(w, "  %-22s %d\n", "not importable:", s.Ignored)
	_, _ = fmt.Fprintf(w, "  %-22s %d\n", "failed:", s.Failed)

	if s.Interrupted {
		_, _ = fmt.Fprintln(w, "  interrupted: not every asset was processed; re-run to resume")
	}

	for _, f := range s.Failures {
		_, _ = fmt.Fprintf(w, "    - %s/%s: %s\n", f.Repo, f.Path, f.Error)
	}
}

func humanBytes(n int64) string {
	const unit = 1024

	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ─── Importer ─────────────────────────────────────────────────────────────────

type importer struct {
	cfg   importConfig
	src   importSource
	tgt   *importTarget
	state *importState
	rep   *importReport
	now   func() time.Time
}

func newImporter(cfg importConfig, src importSource, tgt *importTarget, progress io.Writer) (*importer, error) {
	st, err := openImportState(cfg.StateFile)
	if err != nil {
		return nil, err
	}

	return &importer{
		cfg: cfg, src: src, tgt: tgt, state: st, now: time.Now,
		rep: &importReport{progress: progress, DryRun: cfg.DryRun},
	}, nil
}

// importJob is one asset on its way to a destination repository.
type importJob struct {
	repo     sourceRepo
	dst      string
	asset    sourceAsset
	index    *targetIndex
	layout   [2]string // distribution, component derived from the source layout
	existing bool      // the destination already has something at this path
}

func (im *importer) run(ctx context.Context) *importReport {
	for _, m := range im.cfg.Repos {
		if ctx.Err() != nil {
			break
		}

		im.importRepo(ctx, m)
	}

	if ctx.Err() != nil {
		im.rep.add(func(r *importReport) { r.Interrupted = true })
	}

	return im.rep
}

func (im *importer) importRepo(ctx context.Context, m repoMapping) {
	repo, err := im.src.Repository(ctx, m.Src)
	if err != nil {
		im.rep.fail(m.Src, "", err)
		return
	}

	assets, err := im.src.Assets(ctx, repo)
	if err != nil {
		im.rep.fail(m.Src, "", err)
		return
	}

	todo := im.selectAssets(repo, assets)
	im.rep.logf("%s -> %s [%s]: %d assets, %d to consider", repo.Name, m.Dst, repo.Format, len(assets), len(todo))

	exists, err := im.tgt.ensureRepo(ctx, m.Dst, repo.Format, im.cfg.CreateRepos, im.cfg.DryRun)
	if err != nil {
		im.rep.fail(m.Src, "", err)
		return
	}

	var ix *targetIndex

	if exists {
		if ix, err = im.tgt.loadIndex(ctx, m.Dst, repo.Format); err != nil {
			im.rep.fail(m.Src, "", err)
			return
		}
	} else {
		im.rep.logf("would create hosted %s repository %q", repo.Format, m.Dst)
	}

	dist, comp := "", ""
	if repo.Format == formatDeb {
		dist, comp = deriveAptLayout(assets)
	}

	jobs := make([]importJob, 0, len(todo))
	for _, a := range todo {
		_, existing := ix.byPathOK(a.Path)
		jobs = append(jobs, importJob{repo: repo, dst: m.Dst, asset: a, index: ix, layout: [2]string{dist, comp}, existing: existing})
	}

	im.runJobs(ctx, jobs)
}

// byPathOK reports whether the destination holds anything at path p.
func (ix *targetIndex) byPathOK(p string) (indexEntry, bool) {
	if ix == nil {
		return indexEntry{}, false
	}

	e, ok := ix.byPath[p]

	return e, ok
}

func (im *importer) selectAssets(repo sourceRepo, assets []sourceAsset) []sourceAsset {
	out := make([]sourceAsset, 0, len(assets))

	for _, a := range assets {
		if !strings.HasPrefix(a.Path, im.cfg.PathPrefix) || !importable(repo.Format, a.Path) {
			im.rep.add(func(r *importReport) { r.Ignored++ })
			continue
		}

		out = append(out, a)
	}

	return out
}

func (im *importer) runJobs(ctx context.Context, jobs []importJob) {
	ch := make(chan importJob)

	var wg sync.WaitGroup

	for range im.cfg.Concurrency {
		wg.Go(func() {
			for j := range ch {
				im.handle(ctx, j)
			}
		})
	}

feed:
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-ctx.Done():
			break feed
		}
	}

	close(ch)
	wg.Wait()
}

func (im *importer) handle(ctx context.Context, j importJob) {
	if ctx.Err() != nil {
		return
	}

	a := j.asset
	key := stateKey(j.repo.Name, j.dst, a.Path)

	if a.SHA256 != "" && im.state.isDone(key, a.SHA256) {
		im.rep.add(func(r *importReport) { r.Resumed++ })
		return
	}

	if e, ok := j.index.lookup(a.Path, a.SHA256); ok {
		im.finishPresent(ctx, j, key, e)
		return
	}

	if im.cfg.DryRun {
		im.plan(j)
		return
	}

	if err := im.transfer(ctx, j, key); err != nil {
		im.rep.fail(j.repo.Name, a.Path, err)
	}
}

// plan records what a real run would upload.
func (im *importer) plan(j importJob) {
	a := j.asset
	detail := ""

	if j.repo.Format == formatDeb {
		dist, comp, err := im.debPlacement(j, a)
		if err != nil {
			im.rep.fail(j.repo.Name, a.Path, err)
			return
		}

		detail = fmt.Sprintf(" (distribution=%s component=%s)", dist, comp)
	}

	im.rep.add(func(r *importReport) { r.Uploaded++; r.Bytes += a.Size })
	im.rep.logf("would upload %s/%s -> %s, %s%s", j.repo.Name, a.Path, j.dst, humanBytes(a.Size), detail)
}

// finishPresent handles an asset the destination already has with the same
// content: nothing is transferred, but properties are (re)applied.
func (im *importer) finishPresent(ctx context.Context, j importJob, key string, e indexEntry) {
	a := j.asset

	if im.cfg.DryRun {
		im.rep.add(func(r *importReport) { r.Present++ })
		im.rep.logf("present  %s/%s", j.repo.Name, a.Path)

		return
	}

	if err := im.src.Details(ctx, &a); err != nil {
		im.rep.fail(j.repo.Name, a.Path, err)
		return
	}

	if err := im.applyProperties(ctx, j, a, e.ID); err != nil {
		im.rep.fail(j.repo.Name, a.Path, err)
		return
	}

	im.complete(key, a.SHA256)
	im.rep.add(func(r *importReport) { r.Present++ })
	im.rep.logf("present  %s/%s", j.repo.Name, a.Path)
}

func (im *importer) complete(key, sha string) {
	if sha == "" {
		return
	}

	if err := im.state.markDone(key, sha); err != nil {
		im.rep.logf("warning: %v", err)
	}
}

func (im *importer) applyProperties(ctx context.Context, j importJob, a sourceAsset, id string) error {
	props := selectProperties(a.Props, im.cfg.Properties)
	if len(props) == 0 {
		return nil
	}

	if id == "" {
		return fmt.Errorf("%w: server did not report an artifact id, cannot set properties", errImportVerify)
	}

	if err := im.tgt.setProperties(ctx, j.dst, id, props); err != nil {
		return fmt.Errorf("setting properties: %w", err)
	}

	return nil
}

// debPlacement decides the Debian distribution and component of an asset: its
// own properties win, then the source repository's configuration, then the
// layout of the source's dists/ tree, then the command-line fallbacks.
func (im *importer) debPlacement(j importJob, a sourceAsset) (string, string, error) {
	dist := firstNonEmpty(firstValue(a.Props["deb.distribution"]), j.repo.DebDistribution, j.layout[0], im.cfg.DebDist)
	comp := firstNonEmpty(firstValue(a.Props["deb.component"]), j.repo.DebComponent, j.layout[1], im.cfg.DebComp)

	switch {
	case dist == "":
		return "", "", fmt.Errorf("%w: cannot determine the Debian distribution; pass --deb-distribution", errImportUsage)
	case comp == "":
		return "", "", fmt.Errorf("%w: cannot determine the Debian component; pass --deb-component", errImportUsage)
	}

	return dist, comp, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}

	return ""
}

func (im *importer) uploadMeta(j importJob, a sourceAsset) (uploadMeta, error) {
	m := uploadMeta{}

	// A source date in the future would be rejected by the server; leave the
	// header off and let the upload time stand.
	if !a.Created.IsZero() && !a.Created.After(im.now().Add(-futureSkew)) {
		m.Created = a.Created
	}

	if j.repo.Format == formatDeb {
		var err error
		if m.DebDist, m.DebComponent, err = im.debPlacement(j, a); err != nil {
			return m, err
		}
	}

	return m, nil
}

// transfer streams one asset to the destination, verifies it end to end, and
// applies its properties.
func (im *importer) transfer(ctx context.Context, j importJob, key string) error {
	a := j.asset

	if err := im.src.Details(ctx, &a); err != nil {
		return err
	}

	meta, err := im.uploadMeta(j, a)
	if err != nil {
		return err
	}

	body, err := im.src.Open(ctx, a)
	if err != nil {
		return err
	}

	hr := newHashingReader(body)
	res, upErr := im.tgt.upload(ctx, j.repo.Format, j.dst, a, hr, meta)
	_ = body.Close()

	if upErr != nil {
		return upErr
	}

	got := hr.sums()

	if err := im.verify(ctx, j, a, res, got); err != nil {
		return im.discard(ctx, j, res, err)
	}

	if err := im.applyProperties(ctx, j, a, res.ID); err != nil {
		return err
	}

	sha := a.SHA256
	if sha == "" {
		sha = got.sha256
	}

	im.complete(key, sha)
	im.rep.add(func(r *importReport) { r.Uploaded++; r.Bytes += got.n })
	im.rep.logf("uploaded %s/%s -> %s, %s", j.repo.Name, a.Path, j.dst, humanBytes(got.n))

	return nil
}

// verify compares what was read from the source with the source's own
// checksum, then what the server stored with what was read.
func (im *importer) verify(ctx context.Context, j importJob, a sourceAsset, res uploadResult, got digests) error {
	if a.Size > 0 && got.n != a.Size {
		return fmt.Errorf("%w: size mismatch: source reports %d bytes, received %d", errImportVerify, a.Size, got.n)
	}

	switch {
	case a.SHA256 != "" && a.SHA256 != got.sha256:
		return fmt.Errorf("%w: checksum mismatch: source sha256 %s, received %s", errImportVerify, a.SHA256, got.sha256)
	case a.SHA256 == "" && a.SHA1 != "" && a.SHA1 != got.sha1:
		return fmt.Errorf("%w: checksum mismatch: source sha1 %s, received %s", errImportVerify, a.SHA1, got.sha1)
	}

	stored := res.SHA256

	if stored == "" && j.repo.Format == formatMaven {
		var err error
		if stored, err = im.tgt.serverSHA256(ctx, j.dst, a.Path); err != nil {
			return fmt.Errorf("verifying stored copy: %w", err)
		}
	}

	if stored == "" {
		return fmt.Errorf("%w: server did not report a checksum for the stored copy", errImportVerify)
	}

	if stored != got.sha256 {
		return fmt.Errorf("%w: checksum mismatch after upload: sent sha256 %s, server stored %s", errImportVerify, got.sha256, stored)
	}

	return nil
}

// discard removes a copy that failed verification so it cannot be mistaken for
// a good one, unless it replaced pre-existing content that must not be lost.
func (im *importer) discard(ctx context.Context, j importJob, res uploadResult, cause error) error {
	if j.existing {
		return fmt.Errorf("%w (left in place: it replaced existing content)", cause)
	}

	// The caller's context may be what failed; clean up regardless.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := im.tgt.remove(cctx, j.repo.Format, j.dst, j.asset.Path, res.ID); err != nil {
		return fmt.Errorf("%w (could not remove the bad copy: %w)", cause, err)
	}

	return fmt.Errorf("%w (bad copy removed)", cause)
}

// ─── Hashing ──────────────────────────────────────────────────────────────────

type digests struct {
	sha256 string
	sha1   string
	n      int64
}

// hashingReader digests everything read through it. The HTTP transport may
// finish reading the body on its own goroutine, hence the lock.
type hashingReader struct {
	r  io.Reader
	mu sync.Mutex
	s2 hash.Hash
	s1 hash.Hash
	n  int64
}

func newHashingReader(r io.Reader) *hashingReader {
	return &hashingReader{r: r, s2: sha256.New(), s1: sha1.New()} //nolint:gosec // see import comment
}

func (h *hashingReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	if n > 0 {
		h.mu.Lock()
		_, _ = h.s2.Write(p[:n])
		_, _ = h.s1.Write(p[:n])
		h.n += int64(n)
		h.mu.Unlock()
	}

	return n, err
}

func (h *hashingReader) sums() digests {
	h.mu.Lock()
	defer h.mu.Unlock()

	return digests{sha256: hex.EncodeToString(h.s2.Sum(nil)), sha1: hex.EncodeToString(h.s1.Sum(nil)), n: h.n}
}
