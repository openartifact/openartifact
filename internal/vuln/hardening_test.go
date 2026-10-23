// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

func TestScannerEnv_PassesOnlyAllowlistedVariables(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin", "HOME=/home/oa", "LC_ALL=C", "https_proxy=http://p:3128", "SSL_CERT_FILE=/ca.pem",
		"TRIVY_INSECURE=true", "GRYPE_DB_AUTO_UPDATE=false",
		"OPENARTIFACT_AUTH_HMAC_KEY=hmac", "OPENARTIFACT_DB_PASSWORD=pw", "AWS_SECRET_ACCESS_KEY=aws", "malformed",
	}

	got := scannerEnv(parent, []string{"TRIVY_CACHE_DIR=/c"})

	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/oa", "LC_ALL=C", "https_proxy=http://p:3128", "SSL_CERT_FILE=/ca.pem",
		"TRIVY_INSECURE=true", "GRYPE_DB_AUTO_UPDATE=false", "TRIVY_CACHE_DIR=/c"} {
		if !slices.Contains(got, want) {
			t.Errorf("%q missing from %v", want, got)
		}
	}

	for _, kv := range got {
		if strings.Contains(kv, "hmac") || strings.Contains(kv, "pw") || strings.Contains(kv, "aws") || kv == "malformed" {
			t.Errorf("%q leaked into the scanner environment", kv)
		}
	}
}

func TestRunScanner_DoesNotInheritServerSecrets(t *testing.T) {
	requireSH(t)
	t.Setenv("OPENARTIFACT_AUTH_HMAC_KEY", "s3cret-hmac")
	t.Setenv("TRIVY_TIMEOUT", "5m")

	out, _, err := runScanner(context.Background(), []string{"EXTRA=1"}, "/bin/sh", "-c",
		`echo "[$OPENARTIFACT_AUTH_HMAC_KEY][$TRIVY_TIMEOUT][$EXTRA]"`)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimSpace(string(out)); got != "[][5m][1]" {
		t.Errorf("subprocess saw %s, want [][5m][1]", got)
	}
}

func TestGrypeDB_UnfinishedDownloadIsNotADatabase(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte("db"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("grype-db-download123/vulnerability.db")

	if grypeDBPresent(dir) || !dbMTime(dir).IsZero() {
		t.Fatal("a grype-db-download directory was taken for an activated database")
	}

	write("6/vulnerability.db")

	if !grypeDBPresent(dir) || dbMTime(dir).IsZero() {
		t.Fatal("the schema directory's database was not found")
	}
}

func TestUpdateGrypeDB_RemovesOrphanedDownloads(t *testing.T) {
	requireSH(t)

	log, _ := logger.New("error", "json", "stderr")
	r := NewRegistry(log, Options{CacheDir: t.TempDir()})
	seen := filepath.Join(t.TempDir(), "orphan-seen")

	// The fake grype notes whether the previous run's leftovers were still
	// there, leaves new ones of its own and fails like a killed download.
	installFakeScanner(t, "grype", `[ -e "$GRYPE_DB_CACHE_DIR/grype-db-download9" ] && echo yes > `+seen+`
mkdir -p "$GRYPE_DB_CACHE_DIR/grype-db-download10" && echo partial > "$GRYPE_DB_CACHE_DIR/grype-db-download10/vulnerability.db"
exit 1`)

	orphan := filepath.Join(r.grype.cacheDir, "grype-db-download9")
	if err := os.MkdirAll(orphan, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := r.updateGrypeDB(context.Background()); err == nil {
		t.Fatal("update of a failing grype succeeded")
	}

	if _, err := os.Stat(seen); err == nil {
		t.Error("the previous run's download directory was still there when grype started")
	}

	if matches, _ := filepath.Glob(filepath.Join(r.grype.cacheDir, grypeDownloadGlob)); len(matches) != 0 {
		t.Errorf("unfinished downloads left behind: %v", matches)
	}
}

func lockedTool(t *testing.T) *cliTool {
	t.Helper()

	return &cliTool{name: "trivy", cacheDir: filepath.Join(t.TempDir(), "trivy")}
}

func TestWithLock_WaitsForTheHolderAndHonoursCancellation(t *testing.T) {
	tool := lockedTool(t)

	if err := os.MkdirAll(filepath.Dir(tool.cacheDir), 0o750); err != nil {
		t.Fatal(err)
	}

	unlock, held, err := tryLockFile(tool.lockPath())
	if err != nil || held {
		t.Fatalf("taking the lock: held=%v err=%v", held, err)
	}

	t.Run("cancelled while held", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()

		var ran atomic.Bool

		err := tool.withLock(ctx, func() error {
			ran.Store(true)

			return nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || ran.Load() {
			t.Errorf("err = %v, ran = %v: want a context error and no run", err, ran.Load())
		}
	})

	t.Run("runs once released", func(t *testing.T) {
		done := make(chan error, 1)

		go func() { done <- tool.withLock(context.Background(), func() error { return nil }) }()

		unlock()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("withLock = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the waiter never got the lock after it was released")
		}
	})
}

func TestWithLock_NeverOverlaps(t *testing.T) {
	tool := lockedTool(t)

	var (
		inside, overlaps atomic.Int32
		wg               sync.WaitGroup
	)

	for range 4 {
		wg.Go(func() {
			err := tool.withLock(context.Background(), func() error {
				if inside.Add(1) > 1 {
					overlaps.Add(1)
				}

				// Hold long enough for the others to find the lock taken.
				time.Sleep(20 * time.Millisecond)
				inside.Add(-1)

				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	if overlaps.Load() != 0 {
		t.Errorf("%d overlapping critical sections", overlaps.Load())
	}
}

// stagedTrivy installs a fake trivy that follows mode (a file read on every
// run) and logs what the live database looked like while it ran.
func stagedTrivy(t *testing.T) (*Registry, func(string), string) {
	t.Helper()
	requireSH(t)

	log, _ := logger.New("error", "json", "stderr")
	r := NewRegistry(log, Options{CacheDir: t.TempDir()})
	control := t.TempDir()
	modeFile := filepath.Join(control, "mode")
	runLog := filepath.Join(control, "log")

	installFakeScanner(t, "trivy", `for a in "$@"; do case "$a" in --cache-dir=*) d="${a#--cache-dir=}";; esac; done
live="`+r.trivy.cacheDir+`/db/trivy.db"
if [ -f "$live" ]; then echo "live=$(cat "$live") staged=$(cat "$d/db/trivy.db" 2>/dev/null)" >> `+runLog+`; else echo live=missing >> `+runLog+`; fi
case "$(cat `+modeFile+`)" in
fail) rm -rf "$d/db"; exit 1;;
same) exit 0;;
new) rm -rf "$d/db"; mkdir -p "$d/db"; echo new > "$d/db/trivy.db"; echo '{}' > "$d/db/metadata.json"; exit 0;;
esac`)

	setMode := func(m string) {
		if err := os.WriteFile(modeFile, []byte(m), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	live := filepath.Join(r.trivy.cacheDir, "db")
	if err := os.MkdirAll(live, 0o750); err != nil {
		t.Fatal(err)
	}

	for name, body := range map[string]string{"trivy.db": "old", "metadata.json": "{}"} {
		if err := os.WriteFile(filepath.Join(live, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return r, setMode, runLog
}

func readTrimmed(t *testing.T, p string) string {
	t.Helper()

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return strings.TrimSpace(string(raw))
}

func TestUpdateTrivyDB_StagesAndSwapsInANewDatabase(t *testing.T) {
	r, setMode, runLog := stagedTrivy(t)
	setMode("new")

	if err := r.updateTrivyDB(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The live database was readable while the download ran (Trivy wrote to
	// the staging copy) and holds the new one afterwards.
	if got := readTrimmed(t, runLog); got != "live=old staged=old" {
		t.Errorf("during the run: %q, want the live db untouched and seeded into staging", got)
	}

	if got := readTrimmed(t, filepath.Join(r.trivy.cacheDir, "db", "trivy.db")); got != "new" {
		t.Errorf("live db = %q after the update, want new", got)
	}

	for _, leftover := range []string{trivyStagingDir, "db.old"} {
		if _, err := os.Stat(filepath.Join(r.trivy.cacheDir, leftover)); err == nil {
			t.Errorf("%s left behind", leftover)
		}
	}
}

func TestUpdateTrivyDB_UnchangedDatabaseIsNotSwapped(t *testing.T) {
	r, setMode, _ := stagedTrivy(t)
	setMode("same")

	live := filepath.Join(r.trivy.cacheDir, "db", "trivy.db")

	before, err := os.Stat(live)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.updateTrivyDB(context.Background()); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(live)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("the live database file was replaced although nothing was downloaded (err %v)", err)
	}
}

func TestUpdateTrivyDB_FailureKeepsTheLiveDatabase(t *testing.T) {
	r, setMode, _ := stagedTrivy(t)
	setMode("fail")

	if err := r.updateTrivyDB(context.Background()); err == nil {
		t.Fatal("a failing trivy reported success")
	}

	if got := readTrimmed(t, filepath.Join(r.trivy.cacheDir, "db", "trivy.db")); got != "old" {
		t.Errorf("live db = %q after a failed update, want old", got)
	}

	if _, err := os.Stat(filepath.Join(r.trivy.cacheDir, trivyStagingDir)); err == nil {
		t.Error("staging directory left behind")
	}

	if !trivyDBPresent(r.trivy.cacheDir) {
		t.Error("scans would find no database after the failed update")
	}
}

func TestSwapDirs(t *testing.T) {
	for name, swap := range map[string]func(fresh, live string) error{
		"platform":  swapDirs,
		"by rename": swapDirsByRename,
	} {
		for _, liveExists := range []bool{true, false} {
			t.Run(name, func(t *testing.T) {
				base := t.TempDir()
				fresh, live := filepath.Join(base, "fresh"), filepath.Join(base, "live")

				if err := os.MkdirAll(fresh, 0o750); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(filepath.Join(fresh, "f"), []byte("new"), 0o600); err != nil {
					t.Fatal(err)
				}

				if liveExists {
					if err := os.MkdirAll(live, 0o750); err != nil {
						t.Fatal(err)
					}

					if err := os.WriteFile(filepath.Join(live, "f"), []byte("old"), 0o600); err != nil {
						t.Fatal(err)
					}
				}

				if err := swap(fresh, live); err != nil {
					t.Fatal(err)
				}

				if got := readTrimmed(t, filepath.Join(live, "f")); got != "new" {
					t.Errorf("live = %q, want new", got)
				}

				entries, _ := os.ReadDir(base)
				if len(entries) != 1 {
					t.Errorf("leftovers after the swap: %v", entries)
				}
			})
		}
	}
}

func TestRemoveStale_KeepsFilesAnotherInstallMayBeWriting(t *testing.T) {
	dir := t.TempDir()
	touch := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		when := time.Now().Add(-age)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}

		return p
	}

	oldArchive := touch(".archive-old", time.Hour)
	oldBin := touch(".bin-old", time.Hour)
	youngArchive := touch(".archive-young", time.Second)
	other := touch("trivy", time.Hour)

	removeStale(dir, 10*time.Minute)

	for _, gone := range []string{oldArchive, oldBin} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s survived", filepath.Base(gone))
		}
	}

	for _, kept := range []string{youngArchive, other} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed", filepath.Base(kept))
		}
	}
}

// installLayout creates <root>/trivy/1.0.0/trivy with body and returns root
// (the bin directory) and the binary.
func installLayout(t *testing.T, body string) (string, string) {
	t.Helper()

	root := filepath.Join(t.TempDir(), "bin")
	bin := filepath.Join(root, "trivy", "1.0.0", "trivy")

	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	return root, bin
}

func TestCheckInstallTree(t *testing.T) {
	t.Run("private tree passes", func(t *testing.T) {
		root, bin := installLayout(t, "x")
		if _, err := checkInstallTree(root, bin); err != nil {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("group-writable binary", func(t *testing.T) {
		root, bin := installLayout(t, "x")
		if err := os.Chmod(bin, 0o775); err != nil {
			t.Fatal(err)
		}

		if _, err := checkInstallTree(root, bin); !errors.Is(err, errInsecureInstall) {
			t.Errorf("err = %v, want errInsecureInstall", err)
		}
	})

	t.Run("world-writable directory", func(t *testing.T) {
		root, bin := installLayout(t, "x")
		if err := os.Chmod(filepath.Dir(bin), 0o757); err != nil {
			t.Fatal(err)
		}

		if _, err := checkInstallTree(root, bin); !errors.Is(err, errInsecureInstall) {
			t.Errorf("err = %v, want errInsecureInstall", err)
		}
	})

	t.Run("symlinked directory", func(t *testing.T) {
		root, bin := installLayout(t, "x")
		moved := filepath.Join(filepath.Dir(root), "elsewhere")

		if err := os.Rename(filepath.Join(root, "trivy"), moved); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(moved, filepath.Join(root, "trivy")); err != nil {
			t.Fatal(err)
		}

		if _, err := checkInstallTree(root, bin); !errors.Is(err, errInsecureInstall) {
			t.Errorf("err = %v, want errInsecureInstall", err)
		}
	})

	t.Run("symlinked binary", func(t *testing.T) {
		root, bin := installLayout(t, "x")
		target := filepath.Join(filepath.Dir(root), "other")

		if err := os.WriteFile(target, []byte("y"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.Remove(bin); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(target, bin); err != nil {
			t.Fatal(err)
		}

		if _, err := checkInstallTree(root, bin); !errors.Is(err, errInsecureInstall) {
			t.Errorf("err = %v, want errInsecureInstall", err)
		}
	})

	t.Run("outside the install directory", func(t *testing.T) {
		root, _ := installLayout(t, "x")

		if _, err := checkInstallTree(root, filepath.Join(filepath.Dir(root), "trivy")); !errors.Is(err, errInsecureInstall) {
			t.Errorf("err = %v, want errInsecureInstall", err)
		}
	})
}

func sha(body string) string {
	h := sha256.Sum256([]byte(body))

	return hex.EncodeToString(h[:])
}

func TestVerifiedInstalled_HashesAgainWhenTheFileChanges(t *testing.T) {
	root, bin := installLayout(t, "genuine")

	info, err := os.Lstat(bin)
	if err != nil {
		t.Fatal(err)
	}

	tool := &cliTool{
		name: "trivy", command: "trivy-not-on-any-path", installAuto: true,
		installDir:    filepath.Join(root, "trivy"),
		installedPath: bin, installedSum: sha("genuine"), installedInfo: info,
	}

	lookup := func() string {
		p, _ := tool.lookup()

		return p
	}

	if lookup() != bin {
		t.Fatal("the verified binary is not resolved")
	}

	replace := func(body string, mtime time.Time) {
		t.Helper()

		// A new inode, as an attacker with write access would leave.
		tmp := bin + ".new"
		if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.Chtimes(tmp, mtime, mtime); err != nil {
			t.Fatal(err)
		}

		if err := os.Rename(tmp, bin); err != nil {
			t.Fatal(err)
		}
	}

	replace("tampered", info.ModTime())

	if got := lookup(); got != "" {
		t.Errorf("a replaced binary with another checksum was still resolved: %s", got)
	}

	replace("genuine", info.ModTime())

	if lookup() != bin {
		t.Error("a replaced binary with the pinned checksum was refused")
	}

	if err := os.Chmod(bin, 0o777); err != nil {
		t.Fatal(err)
	}

	if got := lookup(); got != "" {
		t.Errorf("a world-writable binary was still resolved: %s", got)
	}

	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}

	if got := lookup(); got != "" {
		t.Errorf("a removed binary was still resolved: %s", got)
	}
}

func TestStatus_InstalledBinaryGoneIsReportedAsFailedNotInstalling(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	r := f.registry(fakeTrivyScript, nil)

	r.ensureInstalled(context.Background(), &r.trivy, true)

	if st := statusByName(t, r, "trivy"); !st.Available {
		t.Fatalf("status after install = %+v", st)
	}

	if err := os.Remove(installedBinaryPath(r)); err != nil {
		t.Fatal(err)
	}

	st := statusByName(t, r, "trivy")
	if st.Available || st.InstallState != InstallStateFailed || st.Code != CodeScannerInstallFailed || !strings.Contains(st.Message, "missing") {
		t.Errorf("status = %+v, want a failed install that asks for a restart", st)
	}
}

func TestStatus_UnsupportedPlatformHasItsOwnMessage(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	r := f.registry(fakeTrivyScript, nil)
	r.goarch = "s390x-not-pinned"

	r.ensureInstalled(context.Background(), &r.trivy, true)

	st := statusByName(t, r, "trivy")
	if st.Code != CodeScannerInstallFailed || !strings.Contains(st.Message, "No trivy build is pinned for this platform") {
		t.Errorf("status = %+v", st)
	}

	if strings.Contains(st.Message, "outbound access") {
		t.Errorf("the unsupported platform was reported as a network problem: %q", st.Message)
	}
}

func TestInstall_InsecureInstallDirectoryIsRefused(t *testing.T) {
	requireSH(t)
	withoutScannerBinaries(t)

	f := newInstallFixture(t, buildArchive(t, regular("trivy", fakeTrivyScript)))
	r := f.registry(fakeTrivyScript, nil)

	if err := os.MkdirAll(r.trivy.installDir, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(r.trivy.installDir, 0o777); err != nil {
		t.Fatal(err)
	}

	r.ensureInstalled(context.Background(), &r.trivy, true)

	st := statusByName(t, r, "trivy")
	if st.Available || st.InstallState != InstallStateFailed || !strings.Contains(st.Message, "not private") {
		t.Errorf("status = %+v, want a refusal naming the directory permissions", st)
	}
}

func TestNewRegistry_RefusesAutoInstallIntoTheSharedTempDirectory(t *testing.T) {
	withoutScannerBinaries(t)

	// No user cache directory: the default would fall back to os.TempDir().
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")

	if _, temp := defaultCacheDir(); !temp {
		t.Skip("this platform always has a user cache directory")
	}

	log, _ := logger.New("error", "json", "stderr")
	r := NewRegistry(log, Options{Trivy: TrivyOptions{Install: true}, Grype: GrypeOptions{Install: true}})

	for _, name := range []string{"trivy", "grype"} {
		st := statusByName(t, r, name)
		if st.Code != CodeScannerInstallFailed || st.InstallState != InstallStateFailed || !strings.Contains(st.Message, "OPENARTIFACT_SCANNER_CACHE_DIR") {
			t.Errorf("%s status = %+v", name, st)
		}
	}

	if r.trivy.installAuto || r.grype.installAuto {
		t.Error("install auto stayed on with a temp-dir cache")
	}
}
