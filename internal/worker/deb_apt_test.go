// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/gpg"
)

// aptRepo writes the blobs a DebMetadataWorker generated for dist into a
// "file:" repository tree and returns its root.
func aptRepo(t *testing.T, blobs map[string][]byte, skip ...string) string {
	t.Helper()

	root := t.TempDir()

	for path, data := range blobs {
		skipped := false

		for _, s := range skip {
			skipped = skipped || strings.HasSuffix(path, "/"+s)
		}

		if skipped {
			continue
		}

		dst := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

// aptUpdate runs a rootless "apt-get update" against the file: repository at
// repoRoot, trusting only keyPath, and returns its combined output and error.
func aptUpdate(t *testing.T, aptGet, repoRoot, keyPath string) (string, error) {
	t.Helper()

	base := t.TempDir()

	for _, d := range []string{"state/lists/partial", "cache/archives/partial", "etc"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	sources := filepath.Join(base, "etc", "sources.list")
	line := "deb [arch=amd64 signed-by=" + keyPath + "] file:" + repoRoot + " bookworm main\n"

	if err := os.WriteFile(sources, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	status := filepath.Join(base, "status")
	if err := os.WriteFile(status, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), aptGet,
		"-o", "Dir::Etc::sourcelist="+sources,
		"-o", "Dir::Etc::sourceparts=/dev/null",
		"-o", "Dir::Etc::trusted=/dev/null",
		"-o", "Dir::Etc::trustedparts=/dev/null",
		"-o", "Dir::State="+filepath.Join(base, "state"),
		"-o", "Dir::State::status="+status,
		"-o", "Dir::Cache="+filepath.Join(base, "cache"),
		"-o", "Debug::NoLocking=1",
		"-o", "APT::Sandbox::User=",
		"-o", "APT::Architecture=amd64",
		"-o", "APT::Architectures=amd64",
		"update",
	)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")

	out, err := cmd.CombinedOutput()
	t.Logf("apt-get update (err=%v):\n%s", err, out)

	return string(out), err
}

// TestDebMetadataVerifiesWithApt feeds the metadata DebMetadataWorker
// generates (signed Release/InRelease/Release.gpg and the Packages indexes it
// lists) to a real apt-get and requires the signatures to verify. A Release
// whose content was altered after signing must be rejected.
func TestDebMetadataVerifiesWithApt(t *testing.T) {
	aptGet, err := exec.LookPath("apt-get")
	if err != nil {
		t.Skip("apt-get not installed")
	}

	if _, err := exec.LookPath("gpgv"); err != nil {
		t.Skip("gpgv not installed")
	}

	entity, err := gpg.GenerateKey("OpenArtifact Test", "", "test@example.com")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	var priv bytes.Buffer

	pw, _ := armor.Encode(&priv, openpgp.PrivateKeyType, nil)
	if err := entity.SerializePrivate(pw, nil); err != nil {
		t.Fatal(err)
	}

	_ = pw.Close()

	signer, err := gpg.NewLocalSigner(bytes.NewReader(priv.Bytes()), nil)
	if err != nil {
		t.Fatalf("NewLocalSigner: %v", err)
	}

	pub, err := signer.PublicKey()
	if err != nil {
		t.Fatal(err)
	}

	keyPath := filepath.Join(t.TempDir(), "key.asc")
	if err := os.WriteFile(keyPath, pub, 0o644); err != nil {
		t.Fatal(err)
	}

	db := &benchWorkerDB{
		components:    []models.DebianComponent{{Name: "main"}},
		architectures: []models.DebianArchitecture{{Name: "amd64"}},
	}
	w := &DebMetadataWorker{db: db, log: testLogger(t)}
	dist := models.DebianDistribution{ID: "dist-1", Name: "bookworm"}
	entries := []models.DebPackageEntry{{
		ID: "1", Name: "hello", Version: "1.0-1", Arch: "amd64", Filename: "hello_1.0-1_amd64.deb",
		Distribution: "bookworm", ComponentName: "main", SHA256: strings.Repeat("a", 64), Size: 1234,
		ControlFields: map[string]string{"Maintainer": "J <j@example.org>", "Description": "hello"},
	}}

	blobs := make(map[string][]byte)
	if err := w.generateDistribution(context.Background(), dist, entries, signer, blobs); err != nil {
		t.Fatalf("generateDistribution: %v", err)
	}

	t.Run("InRelease", func(t *testing.T) {
		root := aptRepo(t, blobs, "Release", "Release.gpg")

		if out, err := aptUpdate(t, aptGet, root, keyPath); err != nil {
			t.Fatalf("apt-get update with InRelease: %v\n%s", err, out)
		}
	})

	t.Run("Release+Release.gpg", func(t *testing.T) {
		root := aptRepo(t, blobs, "InRelease")

		out, err := aptUpdate(t, aptGet, root, keyPath)
		if err != nil {
			t.Fatalf("apt-get update with Release.gpg: %v\n%s", err, out)
		}

		if strings.Contains(out, "NO_PUBKEY") || strings.Contains(out, "not signed") {
			t.Fatalf("apt did not verify Release.gpg:\n%s", out)
		}
	})

	t.Run("tampered Release is rejected", func(t *testing.T) {
		root := aptRepo(t, blobs, "InRelease")
		release := filepath.Join(root, "dists", "bookworm", "Release")

		data, err := os.ReadFile(release)
		if err != nil {
			t.Fatal(err)
		}

		tampered := bytes.Replace(data, []byte("Suite:"), []byte("Suite: evil-"), 1)
		if bytes.Equal(tampered, data) {
			t.Fatal("test bug: Release has no Suite field to tamper with")
		}

		if err := os.WriteFile(release, tampered, 0o644); err != nil {
			t.Fatal(err)
		}

		if out, err := aptUpdate(t, aptGet, root, keyPath); err == nil {
			t.Fatalf("apt-get update accepted a Release that does not match Release.gpg:\n%s", out)
		}
	})
}
