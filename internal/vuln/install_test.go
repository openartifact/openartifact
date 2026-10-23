// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/pkg/logger"
)

const fakeTrivyScript = "#!/bin/sh\necho trivy fake\n"

type tarEntry struct {
	name     string
	typeflag byte
	body     string
	linkname string
}

func regular(name, body string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeReg, body: body}
}

// buildArchive returns a gzip-compressed tar holding entries.
func buildArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o755, Linkname: e.linkname}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}

		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}

		if e.typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)

	return hex.EncodeToString(h[:])
}

// installFixture serves one archive and pins it in a manifest for this platform.
type installFixture struct {
	t        *testing.T
	srv      *httptest.Server
	requests atomic.Int32
	archive  []byte
	// pinned is the archive the manifest pins; nil means archive itself.
	pinned []byte
	// onRequest, when set, runs for every request with its 1-based number.
	onRequest func(n int32)
	status    atomic.Int32
	// gate, when set, is closed by the test to let a download proceed; started
	// is closed by the handler on the first request.
	gate    chan struct{}
	started chan struct{}
}

func newInstallFixture(t *testing.T, archive []byte) *installFixture {
	t.Helper()

	f := &installFixture{t: t, archive: archive}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.requests.Add(1)

		if f.onRequest != nil {
			f.onRequest(n)
		}

		if f.started != nil {
			select {
			case <-f.started:
			default:
				close(f.started)
			}
		}

		if f.gate != nil {
			<-f.gate
		}

		if code := int(f.status.Load()); code != 0 {
			http.Error(w, "nope", code)
			return
		}

		if r.URL.Path != "/mirror/trivy.tar.gz" {
			http.NotFound(w, r)
			return
		}

		_, _ = w.Write(f.archive)
	}))
	t.Cleanup(f.srv.Close)

	return f
}

// registry builds a registry that installs trivy from the fixture into a
// fresh cache; mutate tweaks the options.
func (f *installFixture) registry(binary string, mutate func(*Options)) *Registry {
	f.t.Helper()

	log, _ := logger.New("error", "json", "stderr")
	opts := Options{
		CacheDir:        f.t.TempDir(),
		DownloadBaseURL: f.srv.URL + "/mirror",
		Trivy:           TrivyOptions{Install: true},
	}

	if mutate != nil {
		mutate(&opts)
	}

	r := NewRegistry(log, opts)
	r.httpClient = f.srv.Client()
	r.retryInitial = time.Millisecond
	r.retryMax = 2 * time.Millisecond
	pinned := f.archive
	if f.pinned != nil {
		pinned = f.pinned
	}

	r.manifest = func() (manifest, error) {
		return manifest{Scanners: map[string]manifestScanner{
			"trivy": {
				Version: "9.9.9",
				Binary:  "trivy",
				Platforms: map[string]manifestBuild{
					runtime.GOOS + "/" + runtime.GOARCH: {
						Path:          "trivy.tar.gz",
						ArchiveSHA256: sum(pinned),
						BinarySHA256:  sum([]byte(binary)),
					},
				},
			},
		}}, nil
	}

	return r
}

func requireSH(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
}

func installedBinaryPath(r *Registry) string {
	return filepath.Join(r.trivy.installDir, "9.9.9", "trivy")
}

func assertNoLeftovers(t *testing.T, r *Registry) {
	t.Helper()

	entries, _ := os.ReadDir(filepath.Join(r.trivy.installDir, "9.9.9"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestInstall_DownloadsVerifiesAndRuns(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t,
		regular("LICENSE", "license text"),
		regular("./trivy", fakeTrivyScript),
		regular("contrib/html.tpl", "tpl"),
	))
	r := f.registry(fakeTrivyScript, nil)

	if st := statusByName(t, r, "trivy"); st.Available || st.Code != CodeScannerInstalling || st.InstallState != InstallStateInstalling {
		t.Fatalf("before the install starts: %+v", st)
	}

	r.ensureInstalled(context.Background(), &r.trivy, true)

	st := statusByName(t, r, "trivy")
	if !st.Available || st.Path != installedBinaryPath(r) || st.InstallState != InstallStateReady {
		t.Fatalf("after install: %+v", st)
	}

	fi, err := os.Stat(installedBinaryPath(r))
	if err != nil {
		t.Fatal(err)
	}

	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
	}

	assertNoLeftovers(t, r)

	// Only the scanner binary is extracted.
	entries, _ := os.ReadDir(filepath.Dir(installedBinaryPath(r)))
	if len(entries) != 1 {
		t.Errorf("install dir holds %d entries, want only the binary", len(entries))
	}
}

func TestInstall_StatusIsInstallingWhileDownloading(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	f.gate = make(chan struct{})
	f.started = make(chan struct{})

	r := f.registry(fakeTrivyScript, nil)
	done := make(chan struct{})

	go func() {
		defer close(done)

		r.ensureInstalled(context.Background(), &r.trivy, true)
	}()

	<-f.started

	if st := statusByName(t, r, "trivy"); st.Available || st.InstallState != InstallStateInstalling || st.Code != CodeScannerInstalling {
		t.Errorf("during download: %+v", st)
	}

	close(f.gate)
	<-done

	if st := statusByName(t, r, "trivy"); !st.Available || st.InstallState != InstallStateReady {
		t.Errorf("after download: %+v", st)
	}
}

func TestInstall_NeverInstallsUnverifiedBytes(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	good := buildArchive(t, regular("trivy", fakeTrivyScript))
	other := buildArchive(t, regular("trivy", "#!/bin/sh\necho evil\n"))

	cases := []struct {
		name    string
		served  []byte
		pinned  []byte // nil: the served archive is the pinned one
		binary  string // the binary content the manifest pins
		wantErr error
	}{
		{"archive differs from the pinned checksum", other, good, fakeTrivyScript, errChecksum},
		{"binary differs from its pinned checksum", good, nil, "#!/bin/sh\necho something else\n", errBinaryChecksum},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstallFixture(t, tc.served)
			f.pinned = tc.pinned
			r := f.registry(tc.binary, nil)
			r.probe = func(context.Context, string, ...string) error {
				t.Error("an unverified binary must never be executed")
				return nil
			}

			_, result, err := r.installTool(context.Background(), &r.trivy, true)
			if !errors.Is(err, tc.wantErr) || result != resultChecksum {
				t.Fatalf("installTool = (%q, %v), want %v", result, err, tc.wantErr)
			}

			if _, err := os.Stat(installedBinaryPath(r)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a binary exists after a failed verification: %v", err)
			}

			assertNoLeftovers(t, r)

			if st := statusByName(t, r, "trivy"); st.Available {
				t.Errorf("trivy became available: %+v", st)
			}
		})
	}
}

func TestInstall_RejectsUnsafeArchives(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	cases := []struct {
		name    string
		entries []tarEntry
		wantErr error
	}{
		{"path traversal", []tarEntry{regular("../evil", "x"), regular("trivy", fakeTrivyScript)}, errUnsafeEntry},
		{"path traversal after the binary", []tarEntry{regular("trivy", fakeTrivyScript), regular("a/../../evil", "x")}, errUnsafeEntry},
		{"absolute path", []tarEntry{regular("/etc/cron.d/x", "x"), regular("trivy", fakeTrivyScript)}, errUnsafeEntry},
		{"binary is a symlink", []tarEntry{{name: "trivy", typeflag: tar.TypeSymlink, linkname: "/bin/sh"}}, errLinkEntry},
		{"hardlink", []tarEntry{regular("trivy", fakeTrivyScript), {name: "x", typeflag: tar.TypeLink, linkname: "trivy"}}, errLinkEntry},
		{"binary is a directory", []tarEntry{{name: "trivy", typeflag: tar.TypeDir}}, errNotRegular},
		{"no binary", []tarEntry{regular("README", "x")}, errBinaryMissing},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The archive is correctly pinned: only its content is wrong.
			f := newInstallFixture(t, buildArchive(t, tc.entries...))
			r := f.registry(fakeTrivyScript, nil)
			r.probe = func(context.Context, string, ...string) error {
				t.Error("nothing may be executed")
				return nil
			}

			parent := filepath.Dir(r.trivy.installDir)

			_, result, err := r.installTool(context.Background(), &r.trivy, true)
			if !errors.Is(err, tc.wantErr) || result != resultExtract {
				t.Fatalf("installTool = (%q, %v), want %v", result, err, tc.wantErr)
			}

			if _, err := os.Stat(installedBinaryPath(r)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("binary installed from an unsafe archive: %v", err)
			}

			assertNoLeftovers(t, r)

			// Nothing escaped the install tree.
			for _, escaped := range []string{filepath.Join(parent, "evil"), filepath.Join(r.opts.CacheDir, "evil")} {
				if _, err := os.Stat(escaped); err == nil {
					t.Errorf("%s was written", escaped)
				}
			}
		})
	}
}

func TestInstall_SizeLimits(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	archive := buildArchive(t, regular("trivy", fakeTrivyScript))

	t.Run("archive", func(t *testing.T) {
		f := newInstallFixture(t, archive)
		r := f.registry(fakeTrivyScript, nil)
		r.maxArchive = 10

		_, result, err := r.installTool(context.Background(), &r.trivy, true)
		if !errors.Is(err, errArchiveSize) || result != resultDownload {
			t.Fatalf("installTool = (%q, %v)", result, err)
		}

		assertNoLeftovers(t, r)
	})

	t.Run("binary", func(t *testing.T) {
		f := newInstallFixture(t, archive)
		r := f.registry(fakeTrivyScript, nil)
		r.maxBinary = 5

		_, result, err := r.installTool(context.Background(), &r.trivy, true)
		if !errors.Is(err, errBinarySize) || result != resultExtract {
			t.Fatalf("installTool = (%q, %v)", result, err)
		}

		if _, err := os.Stat(installedBinaryPath(r)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("oversize binary was installed: %v", err)
		}

		assertNoLeftovers(t, r)
	})
}

func TestInstall_HTTPFailureIsReportedAndRetried(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	f.status.Store(http.StatusNotFound)

	r := f.registry(fakeTrivyScript, nil)

	if _, result, err := r.installTool(context.Background(), &r.trivy, true); !errors.Is(err, errDownloadStatus) || result != resultDownload {
		t.Fatalf("installTool = (%q, %v)", result, err)
	}

	// The installer keeps retrying with backoff until the mirror recovers.
	f.status.Store(0)

	r.ensureInstalled(context.Background(), &r.trivy, true)

	if st := statusByName(t, r, "trivy"); !st.Available {
		t.Errorf("trivy not installed after the mirror recovered: %+v", st)
	}
}

func TestInstall_FailedStateIsReported(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	f.status.Store(http.StatusInternalServerError)

	r := f.registry(fakeTrivyScript, nil)
	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	// Stop retrying after the second failed attempt; the failure state stays.
	// The state is recorded before the next attempt begins, so cancelling from
	// the third request (which only starts after the second failure) is safe.
	f.onRequest = func(n int32) {
		if n == 3 {
			cancel()
		}
	}

	r.ensureInstalled(ctx, &r.trivy, true)

	st := statusByName(t, r, "trivy")
	if st.Available || st.InstallState != InstallStateFailed || st.Code != CodeScannerInstallFailed {
		t.Fatalf("status = %+v", st)
	}

	if strings.Contains(st.Message, f.srv.URL) || strings.Contains(st.Message, r.opts.CacheDir) {
		t.Errorf("message %q leaks the URL or a path", st.Message)
	}
}

func TestInstall_PrecedenceConfiguredPathThenPATHThenInstall(t *testing.T) {
	requireSH(t)

	archive := buildArchive(t, regular("trivy", fakeTrivyScript))

	t.Run("a configured path is never replaced by an install", func(t *testing.T) {
		withoutScannerBinaries(t)

		f := newInstallFixture(t, archive)
		missing := filepath.Join(t.TempDir(), "trivy")
		r := f.registry(fakeTrivyScript, func(o *Options) { o.Trivy.Path = missing })

		r.runTool(context.Background(), &r.trivy, func(context.Context) error { return nil })

		if n := f.requests.Load(); n != 0 {
			t.Errorf("%d downloads despite a configured path", n)
		}

		if st := statusByName(t, r, "trivy"); st.Available || st.Code != CodeScannerUnavailable || st.InstallState != "" {
			t.Errorf("status = %+v", st)
		}
	})

	t.Run("a binary on PATH is used and nothing is downloaded", func(t *testing.T) {
		withoutScannerBinaries(t)
		installFakeScanner(t, "trivy", "echo on path")

		f := newInstallFixture(t, archive)
		r := f.registry(fakeTrivyScript, nil)

		r.ensureInstalled(context.Background(), &r.trivy, true)

		if n := f.requests.Load(); n != 0 {
			t.Errorf("%d downloads although trivy is on PATH", n)
		}

		st := statusByName(t, r, "trivy")
		if !st.Available || filepath.Base(st.Path) != "trivy" || strings.HasPrefix(st.Path, r.opts.CacheDir) || st.InstallState != "" {
			t.Errorf("status = %+v", st)
		}
	})

	t.Run("an installed copy is used only when PATH has none", func(t *testing.T) {
		withoutScannerBinaries(t)

		f := newInstallFixture(t, archive)
		r := f.registry(fakeTrivyScript, nil)
		r.ensureInstalled(context.Background(), &r.trivy, true)

		if st := statusByName(t, r, "trivy"); st.Path != installedBinaryPath(r) {
			t.Fatalf("installed path = %q", st.Path)
		}

		installFakeScanner(t, "trivy", "echo on path")

		if st := statusByName(t, r, "trivy"); st.Path == installedBinaryPath(r) {
			t.Errorf("PATH must win over the installed copy: %+v", st)
		}
	})
}

func TestInstall_ReusesVerifiedCopyAndReplacesCorruptOne(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	first := f.registry(fakeTrivyScript, nil)
	first.ensureInstalled(context.Background(), &first.trivy, true)

	if f.requests.Load() != 1 {
		t.Fatalf("first install made %d requests", f.requests.Load())
	}

	// A restart: a new registry over the same cache must not download again.
	cache := first.opts.CacheDir
	second := f.registry(fakeTrivyScript, func(o *Options) { o.CacheDir = cache })
	second.ensureInstalled(context.Background(), &second.trivy, true)

	if n := f.requests.Load(); n != 1 {
		t.Errorf("restart downloaded again (%d requests)", n)
	}

	if st := statusByName(t, second, "trivy"); !st.Available || st.InstallState != InstallStateReady {
		t.Errorf("status after reuse = %+v", st)
	}

	// Offline start: adopts the verified copy, downloads nothing.
	offline := f.registry(fakeTrivyScript, func(o *Options) { o.CacheDir = cache; o.Offline = true })
	offline.ensureInstalled(context.Background(), &offline.trivy, false)

	if st := statusByName(t, offline, "trivy"); !st.Available {
		t.Errorf("offline start did not adopt the installed copy: %+v", st)
	}

	// A modified copy fails the re-hash: it is never executed, and replaced.
	if err := os.WriteFile(installedBinaryPath(second), []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	executed := 0
	third := f.registry(fakeTrivyScript, func(o *Options) { o.CacheDir = cache })
	probe := third.probe
	third.probe = func(ctx context.Context, bin string, args ...string) error {
		executed++

		got, err := os.ReadFile(bin)
		if err != nil || string(got) != fakeTrivyScript {
			t.Errorf("the tampered binary was executed: %q (%v)", got, err)
		}

		return probe(ctx, bin, args...)
	}

	third.ensureInstalled(context.Background(), &third.trivy, true)

	if n := f.requests.Load(); n != 2 {
		t.Errorf("requests = %d, want a re-download of the corrupt copy", n)
	}

	if executed != 1 {
		t.Errorf("probe ran %d times, want once on the replacement", executed)
	}

	if got, _ := os.ReadFile(installedBinaryPath(third)); string(got) != fakeTrivyScript {
		t.Errorf("binary after repair = %q", got)
	}
}

func TestInstall_NoexecIsReported(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	r := f.registry(fakeTrivyScript, nil)
	r.probe = func(_ context.Context, bin string, _ ...string) error {
		return &fs.PathError{Op: "fork/exec", Path: bin, Err: syscall.EACCES}
	}

	r.ensureInstalled(context.Background(), &r.trivy, true)

	if n := f.requests.Load(); n != 1 {
		t.Errorf("a noexec failure must not be retried (requests = %d)", n)
	}

	st := statusByName(t, r, "trivy")
	if st.Available || st.Ready || st.Code != CodeScannerInstallNoexec || st.InstallState != InstallStateFailed {
		t.Fatalf("status = %+v", st)
	}

	if !strings.Contains(st.Message, "noexec") {
		t.Errorf("message %q does not mention noexec", st.Message)
	}

	// A scan reports the scanner as unavailable instead of running the binary.
	if _, pre := r.trivy.preflight(); pre == nil || pre.Kind != FailureUnavailable {
		t.Errorf("preflight = %v", pre)
	}
}

func TestInstall_UnsupportedPlatform(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	r := f.registry(fakeTrivyScript, nil)
	r.goarch = "s390x-not-pinned"

	r.ensureInstalled(context.Background(), &r.trivy, true)

	if n := f.requests.Load(); n != 0 {
		t.Errorf("%d downloads for a platform without a pinned build", n)
	}

	if st := statusByName(t, r, "trivy"); st.Available || st.Code != CodeScannerInstallFailed || st.InstallState != InstallStateFailed {
		t.Errorf("status = %+v", st)
	}
}

func TestInstall_OfflineAndNoneNeverDownload(t *testing.T) {
	requireSH(t)

	archive := buildArchive(t, regular("trivy", fakeTrivyScript))

	t.Run("offline", func(t *testing.T) {
		withoutScannerBinaries(t)

		f := newInstallFixture(t, archive)
		r := f.registry(fakeTrivyScript, func(o *Options) { o.Offline = true })

		// Synchronous: a Start/Stop pair could be cancelled before the
		// scanner's goroutine reaches the guard under test.
		r.runTool(context.Background(), &r.trivy, r.updateTrivyDB)

		if n := f.requests.Load(); n != 0 {
			t.Errorf("%d downloads in offline mode", n)
		}

		st := statusByName(t, r, "trivy")
		if st.Available || !strings.Contains(st.Message, "Offline") || st.InstallState != "" {
			t.Errorf("status = %+v", st)
		}
	})

	t.Run("install none", func(t *testing.T) {
		withoutScannerBinaries(t)

		f := newInstallFixture(t, archive)
		r := f.registry(fakeTrivyScript, func(o *Options) { o.Trivy.Install = false })

		// Synchronous: a Start/Stop pair could be cancelled before the
		// scanner's goroutine reaches the guard under test.
		r.runTool(context.Background(), &r.trivy, r.updateTrivyDB)

		if n := f.requests.Load(); n != 0 {
			t.Errorf("%d downloads with install none", n)
		}

		st := statusByName(t, r, "trivy")
		if st.Available || st.InstallState != "" || !strings.Contains(st.Message, "OPENARTIFACT_TRIVY_INSTALL=auto") {
			t.Errorf("status = %+v", st)
		}
	})
}

func TestInstall_StartInstallsThenRefreshesDatabase(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	// The fake binary acts as "trivy image --download-db-only": it seeds the
	// database the updater waits for.
	script := "#!/bin/sh\nPATH=/usr/bin:/bin\nfor a in \"$@\"; do case \"$a\" in --cache-dir=*) d=\"${a#--cache-dir=}\";; esac; done\n" +
		"if [ -n \"$d\" ]; then mkdir -p \"$d/db\" && : > \"$d/db/trivy.db\" && echo '{}' > \"$d/db/metadata.json\"; fi\n"
	f := newInstallFixture(t, buildArchive(t, regular("trivy", script)))
	r := f.registry(script, nil)

	r.Start(context.Background())
	defer r.Stop()

	waitFor(t, "trivy to be installed and its database downloaded", func() bool {
		return statusByName(t, r, "trivy").Ready
	})

	st := statusByName(t, r, "trivy")
	if st.Path != installedBinaryPath(r) || st.InstallState != InstallStateReady || st.DBState != DBStateReady {
		t.Errorf("status = %+v", st)
	}
}

func TestScannersManifest_Shape(t *testing.T) {
	m, err := embeddedManifest()
	if err != nil {
		t.Fatal(err)
	}

	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	repos := map[string]string{"trivy": "aquasecurity/trivy", "grype": "anchore/grype"}

	for name, repo := range repos {
		s, ok := m.Scanners[name]
		if !ok {
			t.Fatalf("manifest has no %s", name)
		}

		if s.Binary != name || !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(s.Version) {
			t.Errorf("%s: binary %q version %q", name, s.Binary, s.Version)
		}

		for _, platform := range []string{"linux/amd64", "linux/arm64"} {
			b, ok := s.Platforms[platform]
			if !ok {
				t.Errorf("%s has no %s build", name, platform)
				continue
			}

			if !hex64.MatchString(b.ArchiveSHA256) || !hex64.MatchString(b.BinarySHA256) || b.ArchiveSHA256 == b.BinarySHA256 {
				t.Errorf("%s %s: bad checksums %+v", name, platform, b)
			}

			wantPrefix := repo + "/releases/download/v" + s.Version + "/"
			if !strings.HasPrefix(b.Path, wantPrefix) || !strings.HasSuffix(b.Path, ".tar.gz") || strings.Contains(b.Path, "..") {
				t.Errorf("%s %s: path %q", name, platform, b.Path)
			}
		}
	}
}

// The runtime download and the "scanners" image variant must ship the same
// release: both come from docker/Dockerfile's pinned upstream images.
func TestScannersManifest_MatchesDockerfilePins(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docker", "Dockerfile"))
	if err != nil {
		t.Skipf("Dockerfile not available: %v", err)
	}

	m, err := embeddedManifest()
	if err != nil {
		t.Fatal(err)
	}

	for name, re := range map[string]*regexp.Regexp{
		"trivy": regexp.MustCompile(`(?m)^FROM \S*/trivy:v?(\d+\.\d+\.\d+)@sha256:[0-9a-f]{64} AS trivy-src$`),
		"grype": regexp.MustCompile(`(?m)^FROM \S*/grype:v?(\d+\.\d+\.\d+)@sha256:[0-9a-f]{64} AS grype-src$`),
	} {
		match := re.FindSubmatch(raw)
		if match == nil {
			t.Fatalf("no pinned %s image in the Dockerfile", name)
		}

		if got := m.Scanners[name].Version; got != string(match[1]) {
			t.Errorf("%s: manifest pins %s but the Dockerfile pins %s: run scripts/update-scanner-manifest.sh", name, got, match[1])
		}
	}
}

func TestInstall_CountsAttemptsByScannerAndResult(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	reg := metricsForTests()
	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	r := f.registry(fakeTrivyScript, nil).WithMetrics(reg)

	f.status.Store(http.StatusBadGateway)

	// One failed attempt, then the mirror recovers: two attempts in total.
	f.onRequest = func(n int32) {
		if n == 2 {
			f.status.Store(0)
		}
	}

	r.ensureInstalled(context.Background(), &r.trivy, true)

	counter := func(result string) float64 {
		return testutil.ToFloat64(reg.ScannerInstallAttemptsTotal.WithLabelValues("trivy", result))
	}

	if counter(resultDownload) != 1 || counter(resultInstalled) != 1 {
		t.Errorf("download_failed=%v installed=%v, want 1 and 1", counter(resultDownload), counter(resultInstalled))
	}
}

// metricsForTests creates the metrics registry once per process: it
// registers on Prometheus' default registry, which refuses a second one.
//
//nolint:gochecknoglobals // shared test fixture
var metricsForTests = sync.OnceValue(metrics.NewRegistry)

func TestDBUpdates_AreCountedAndTimestamped(t *testing.T) {
	log, _ := logger.New("error", "json", "stderr")
	r := NewRegistry(log, Options{CacheDir: t.TempDir()}).WithMetrics(metricsForTests())
	r.retryInitial = time.Millisecond

	attempts := func(result string) float64 {
		return testutil.ToFloat64(r.metrics.ScannerDBUpdateAttemptsTotal.WithLabelValues("trivy", result))
	}
	retry := r.retryInitial

	r.refreshOnce(context.Background(), &r.trivy, func(context.Context) error { return errors.New("mirror down") }, &retry)
	r.refreshOnce(context.Background(), &r.trivy, func(context.Context) error { return nil }, &retry)

	if attempts("failure") != 1 || attempts("success") != 1 {
		t.Errorf("failure=%v success=%v, want 1 and 1", attempts("failure"), attempts("success"))
	}

	if ts := testutil.ToFloat64(r.metrics.ScannerDBLastSuccessTimestamp.WithLabelValues("trivy")); time.Since(time.Unix(int64(ts), 0)) > time.Minute {
		t.Errorf("last success timestamp = %v, want now", ts)
	}

	// A refresh cut short by shutdown is neither.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	r.refreshOnce(cancelled, &r.trivy, func(context.Context) error { return context.Canceled }, &retry)

	if attempts("failure") != 1 {
		t.Errorf("failure = %v after a cancelled update, want 1", attempts("failure"))
	}

	// After a restart the gauge starts from the database already on disk.
	grype := r.grype.cacheDir
	if err := os.MkdirAll(filepath.Join(grype, "6"), 0o750); err != nil {
		t.Fatal(err)
	}

	db := filepath.Join(grype, "6", "vulnerability.db")
	when := time.Now().Add(-48 * time.Hour).Truncate(time.Second)

	if err := os.WriteFile(db, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chtimes(db, when, when); err != nil {
		t.Fatal(err)
	}

	r.refreshLoop(cancelled, &r.grype, func(context.Context) error { return nil })

	if got := testutil.ToFloat64(r.metrics.ScannerDBLastSuccessTimestamp.WithLabelValues("grype")); int64(got) != when.Unix() {
		t.Errorf("grype gauge = %v, want the database mtime %v", got, when.Unix())
	}
}
